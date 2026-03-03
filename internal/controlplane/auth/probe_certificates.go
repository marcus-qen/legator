package auth

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ProbeCertificateRecord tracks probe client certificates approved for mTLS auth.
// Multiple active certificates per probe are allowed to support rotation overlap.
type ProbeCertificateRecord struct {
	ID                string    `json:"id"`
	ProbeID           string    `json:"probe_id"`
	FingerprintSHA256 string    `json:"fingerprint_sha256"`
	SerialNumber      string    `json:"serial_number,omitempty"`
	Subject           string    `json:"subject,omitempty"`
	CommonName        string    `json:"common_name,omitempty"`
	Issuer            string    `json:"issuer,omitempty"`
	Source            string    `json:"source,omitempty"`
	NotBefore         time.Time `json:"not_before"`
	NotAfter          time.Time `json:"not_after"`
	CreatedAt         time.Time `json:"created_at"`
	RevokedAt         time.Time `json:"revoked_at,omitempty"`
}

func (r ProbeCertificateRecord) Active(at time.Time) bool {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if !r.RevokedAt.IsZero() {
		return false
	}
	if at.Before(r.NotBefore) {
		return false
	}
	if at.After(r.NotAfter) {
		return false
	}
	return true
}

type ProbeCertificateRegistry struct {
	mu      sync.RWMutex
	records map[string]map[string]ProbeCertificateRecord // probe_id -> fingerprint -> record
	now     func() time.Time
}

func NewProbeCertificateRegistry() *ProbeCertificateRegistry {
	return &ProbeCertificateRegistry{
		records: map[string]map[string]ProbeCertificateRecord{},
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func normalizeProbeID(probeID string) string {
	return strings.TrimSpace(probeID)
}

func normalizeFingerprint(fingerprint string) string {
	return strings.ToLower(strings.TrimSpace(fingerprint))
}

func CertificateFingerprintSHA256(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return strings.ToLower(hex.EncodeToString(sum[:]))
}

func probeCertificateRecordFromX509(probeID, source string, cert *x509.Certificate, now time.Time) (ProbeCertificateRecord, error) {
	if cert == nil {
		return ProbeCertificateRecord{}, fmt.Errorf("certificate is required")
	}
	probeID = normalizeProbeID(probeID)
	if probeID == "" {
		return ProbeCertificateRecord{}, fmt.Errorf("probe id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	fingerprint := CertificateFingerprintSHA256(cert)
	if fingerprint == "" {
		return ProbeCertificateRecord{}, fmt.Errorf("empty certificate fingerprint")
	}
	return ProbeCertificateRecord{
		ID:                uuid.New().String(),
		ProbeID:           probeID,
		FingerprintSHA256: fingerprint,
		SerialNumber:      cert.SerialNumber.Text(16),
		Subject:           cert.Subject.String(),
		CommonName:        strings.TrimSpace(cert.Subject.CommonName),
		Issuer:            cert.Issuer.String(),
		Source:            strings.TrimSpace(source),
		NotBefore:         cert.NotBefore.UTC(),
		NotAfter:          cert.NotAfter.UTC(),
		CreatedAt:         now,
	}, nil
}

// Register records (or refreshes) a probe certificate for mTLS auth.
func (r *ProbeCertificateRegistry) Register(probeID, source string, cert *x509.Certificate) (ProbeCertificateRecord, error) {
	rec, err := probeCertificateRecordFromX509(probeID, source, cert, r.now())
	if err != nil {
		return ProbeCertificateRecord{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pm := r.records[rec.ProbeID]
	if pm == nil {
		pm = map[string]ProbeCertificateRecord{}
		r.records[rec.ProbeID] = pm
	}

	if existing, ok := pm[rec.FingerprintSHA256]; ok {
		rec.ID = existing.ID
		rec.CreatedAt = existing.CreatedAt
	}
	pm[rec.FingerprintSHA256] = rec
	return rec, nil
}

// Match returns the matching active cert registration for the probe.
func (r *ProbeCertificateRegistry) Match(probeID string, cert *x509.Certificate, at time.Time) (ProbeCertificateRecord, bool) {
	probeID = normalizeProbeID(probeID)
	fingerprint := normalizeFingerprint(CertificateFingerprintSHA256(cert))
	if probeID == "" || fingerprint == "" {
		return ProbeCertificateRecord{}, false
	}
	if at.IsZero() {
		at = r.now()
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	pm := r.records[probeID]
	if pm == nil {
		return ProbeCertificateRecord{}, false
	}
	rec, ok := pm[fingerprint]
	if !ok {
		return ProbeCertificateRecord{}, false
	}
	if !rec.Active(at) {
		return rec, false
	}
	return rec, true
}

// Revoke marks a probe cert record revoked by fingerprint.
func (r *ProbeCertificateRegistry) Revoke(probeID, fingerprint string) bool {
	probeID = normalizeProbeID(probeID)
	fingerprint = normalizeFingerprint(fingerprint)
	if probeID == "" || fingerprint == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pm := r.records[probeID]
	if pm == nil {
		return false
	}
	rec, ok := pm[fingerprint]
	if !ok {
		return false
	}
	if rec.RevokedAt.IsZero() {
		rec.RevokedAt = r.now()
		pm[fingerprint] = rec
	}
	return true
}

func (r *ProbeCertificateRegistry) List(probeID string) []ProbeCertificateRecord {
	probeID = normalizeProbeID(probeID)
	if probeID == "" {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	pm := r.records[probeID]
	if len(pm) == 0 {
		return nil
	}
	out := make([]ProbeCertificateRecord, 0, len(pm))
	for _, rec := range pm {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NotAfter.Equal(out[j].NotAfter) {
			return out[i].FingerprintSHA256 < out[j].FingerprintSHA256
		}
		return out[i].NotAfter.Before(out[j].NotAfter)
	})
	return out
}
