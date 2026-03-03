package server

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"go.uber.org/zap"
)

func (s *Server) initProbeAuthentication() error {
	s.probeCertRegistry = auth.NewProbeCertificateRegistry()

	mode := s.cfg.ProbeMTLS.ModeOrDefault()
	var certVerifier *auth.ProbeClientCertVerifier
	if mode != auth.ProbeAuthModeOff {
		if !s.cfg.HasTLS() {
			return fmt.Errorf("probe_mtls.mode=%s requires tls_cert and tls_key", mode)
		}
		verifier, err := auth.NewProbeClientCertVerifier(s.cfg.ProbeMTLS.ClientCAPath, s.cfg.ProbeMTLS.ClientCAPEM)
		if err != nil {
			return fmt.Errorf("configure probe mTLS client CA: %w", err)
		}
		certVerifier = verifier
	}

	s.probeAuth = auth.NewProbeAuthenticator(auth.ProbeAuthenticatorConfig{
		Mode:             mode,
		CertVerifier:     certVerifier,
		CertRegistry:     s.probeCertRegistry,
		VerifyAPIKey:     s.validateProbeAPIKey,
		AllowAPIFallback: mode != auth.ProbeAuthModeRequired,
	})

	if shouldInitProbeCertIssuer(s.cfg.ProbeMTLS) {
		issuer, err := auth.NewProbeCertificateIssuerFromMaterial(
			s.cfg.ProbeMTLS.IssuerCertPath,
			s.cfg.ProbeMTLS.IssuerCertPEM,
			s.cfg.ProbeMTLS.IssuerKeyPath,
			s.cfg.ProbeMTLS.IssuerKeyPEM,
			s.cfg.ProbeMTLS.IssueTTLDuration(),
		)
		if err != nil {
			return fmt.Errorf("configure probe certificate issuer: %w", err)
		}
		s.probeCertIssuer = issuer
	}

	s.logger.Info("probe auth configured",
		zap.String("mode", mode),
		zap.Bool("issuer_enabled", s.probeCertIssuer != nil),
	)

	return nil
}

func shouldInitProbeCertIssuer(cfg config.ProbeMTLSConfig) bool {
	return strings.TrimSpace(cfg.IssuerCertPath) != "" ||
		strings.TrimSpace(cfg.IssuerCertPEM) != "" ||
		strings.TrimSpace(cfg.IssuerKeyPath) != "" ||
		strings.TrimSpace(cfg.IssuerKeyPEM) != ""
}

func (s *Server) validateProbeAPIKey(probeID, bearerToken string) bool {
	ps, ok := s.fleetMgr.Get(probeID)
	if !ok {
		return false
	}
	if ps.APIKey == "" {
		return false
	}
	return ps.APIKey == bearerToken
}

func (s *Server) probeHandshakeAuthorizer() cpws.ProbeHandshakeAuthorizer {
	return func(r *http.Request, probeID, bearerToken string) cpws.ProbeHandshakeDecision {
		outcome := s.probeAuth.Authenticate(probeID, bearerToken, r.TLS)
		s.recordProbeCertificateAuthAudit(probeID, outcome)
		if outcome.Allowed {
			return cpws.ProbeHandshakeDecision{Allowed: true}
		}
		body, _ := json.Marshal(map[string]string{"error": outcome.Message})
		if outcome.StatusCode == 0 {
			outcome.StatusCode = http.StatusForbidden
		}
		return cpws.ProbeHandshakeDecision{Allowed: false, StatusCode: outcome.StatusCode, Body: string(body)}
	}
}

func (s *Server) recordProbeCertificateAuthAudit(probeID string, outcome auth.ProbeAuthOutcome) {
	if outcome.Method != auth.ProbeAuthMethodMTLS {
		return
	}

	detail := map[string]any{
		"reason": outcome.Reason,
		"method": outcome.Method,
	}
	if outcome.Certificate != nil {
		detail["fingerprint_sha256"] = outcome.Certificate.FingerprintSHA256
		detail["serial_number"] = outcome.Certificate.SerialNumber
		detail["not_before"] = outcome.Certificate.NotBefore.Format(time.RFC3339)
		detail["not_after"] = outcome.Certificate.NotAfter.Format(time.RFC3339)
	}

	typ := audit.EventProbeCertificateAuthFailed
	summary := "Probe mTLS certificate authentication failed"
	if outcome.Allowed {
		typ = audit.EventProbeCertificateAuthSucceeded
		summary = "Probe authenticated using mTLS certificate"
	} else if outcome.Reason == "certificate_expired" || outcome.Reason == "certificate_invalid" {
		typ = audit.EventProbeCertificateError
		summary = "Probe certificate validation error"
	}

	s.recordAudit(audit.Event{
		Type:    typ,
		ProbeID: probeID,
		Actor:   "probe",
		Summary: summary,
		Detail:  detail,
	})
}

func (s *Server) handleIssueProbeCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	if s.probeCertIssuer == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "probe certificate issuer is not configured")
		return
	}

	probeID := r.PathValue("id")
	if _, ok := s.fleetMgr.Get(probeID); !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	var req struct {
		CommonName string `json:"common_name"`
		ValidFor   string `json:"valid_for"`
		Source     string `json:"source"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	validFor := time.Duration(0)
	if strings.TrimSpace(req.ValidFor) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(req.ValidFor))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "valid_for must be a Go duration string")
			return
		}
		validFor = d
	}

	issued, cert, err := s.probeCertIssuer.Issue(auth.ProbeCertificateIssueRequest{
		ProbeID:    probeID,
		CommonName: req.CommonName,
		ValidFor:   validFor,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "issuer_error", err.Error())
		return
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "issued"
	}
	rec, err := s.probeCertRegistry.Register(probeID, source, cert)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	s.recordAudit(audit.Event{
		Type:    audit.EventProbeCertificateIssued,
		ProbeID: probeID,
		Actor:   "api",
		Summary: "Probe certificate issued",
		Detail: map[string]any{
			"fingerprint_sha256": rec.FingerprintSHA256,
			"expires_at":         rec.NotAfter.Format(time.RFC3339),
			"source":             rec.Source,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"probe_id":       probeID,
		"registration":   rec,
		"certificate":    issued,
		"overlap_notice": "existing active certificates remain valid until expiry/revocation",
	})
}

func (s *Server) handleRegisterProbeCertificate(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}

	probeID := r.PathValue("id")
	if _, ok := s.fleetMgr.Get(probeID); !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	var req struct {
		CertificatePEM string `json:"certificate_pem"`
		Source         string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	chain, err := parseCertificateChain(req.CertificatePEM)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	leaf := chain[0]

	if s.probeAuth != nil && s.probeAuth.Mode() != auth.ProbeAuthModeOff {
		if verifier, err := auth.NewProbeClientCertVerifier(s.cfg.ProbeMTLS.ClientCAPath, s.cfg.ProbeMTLS.ClientCAPEM); err == nil {
			if err := verifier.Verify(leaf, chain[1:], time.Now().UTC()); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid_certificate", err.Error())
				return
			}
		}
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "manual"
	}
	rec, err := s.probeCertRegistry.Register(probeID, source, leaf)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	s.recordAudit(audit.Event{
		Type:    audit.EventProbeCertificateRegistered,
		ProbeID: probeID,
		Actor:   "api",
		Summary: "Probe certificate registered",
		Detail: map[string]any{
			"fingerprint_sha256": rec.FingerprintSHA256,
			"expires_at":         rec.NotAfter.Format(time.RFC3339),
			"source":             rec.Source,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"probe_id": probeID, "registration": rec})
}

func (s *Server) handleListProbeCertificates(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}

	probeID := r.PathValue("id")
	if _, ok := s.fleetMgr.Get(probeID); !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	now := time.Now().UTC()
	items := s.probeCertRegistry.List(probeID)
	rows := make([]map[string]any, 0, len(items))
	for _, rec := range items {
		rows = append(rows, map[string]any{
			"id":                 rec.ID,
			"fingerprint_sha256": rec.FingerprintSHA256,
			"serial_number":      rec.SerialNumber,
			"subject":            rec.Subject,
			"issuer":             rec.Issuer,
			"source":             rec.Source,
			"not_before":         rec.NotBefore,
			"not_after":          rec.NotAfter,
			"revoked_at":         rec.RevokedAt,
			"active":             rec.Active(now),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"probe_id":       probeID,
		"mode":           s.cfg.ProbeMTLS.ModeOrDefault(),
		"total":          len(rows),
		"certificates":   rows,
		"rotation_model": "multiple active certificates are supported for overlap",
	})
}

func parseCertificateChain(chainPEM string) ([]*x509.Certificate, error) {
	chainPEM = strings.TrimSpace(chainPEM)
	if chainPEM == "" {
		return nil, fmt.Errorf("certificate_pem is required")
	}
	out := make([]*x509.Certificate, 0)
	remaining := []byte(chainPEM)
	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		out = append(out, cert)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no certificate PEM blocks found")
	}
	return out, nil
}
