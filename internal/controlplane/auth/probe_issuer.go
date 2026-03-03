package auth

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"
)

type ProbeCertificateIssueRequest struct {
	ProbeID    string
	CommonName string
	ValidFor   time.Duration
}

type ProbeCertificateIssueResult struct {
	CertificatePEM       string    `json:"certificate_pem"`
	PrivateKeyPEM        string    `json:"private_key_pem"`
	CACertificatePEM     string    `json:"ca_certificate_pem"`
	FingerprintSHA256    string    `json:"fingerprint_sha256"`
	SerialNumber         string    `json:"serial_number"`
	NotBefore            time.Time `json:"not_before"`
	NotAfter             time.Time `json:"not_after"`
	SuggestedRotateAfter time.Time `json:"suggested_rotate_after"`
}

type ProbeCertificateIssuer struct {
	caCert     *x509.Certificate
	caKey      crypto.Signer
	caCertPEM  string
	defaultTTL time.Duration
	now        func() time.Time
}

func loadPEMFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func NewProbeCertificateIssuer(caCertPEM, caKeyPEM string, defaultTTL time.Duration) (*ProbeCertificateIssuer, error) {
	caCertPEM = strings.TrimSpace(caCertPEM)
	caKeyPEM = strings.TrimSpace(caKeyPEM)
	if caCertPEM == "" || caKeyPEM == "" {
		return nil, fmt.Errorf("issuer certificate and key are required")
	}

	cert, err := parseCertificatePEM(caCertPEM)
	if err != nil {
		return nil, fmt.Errorf("parse issuer certificate: %w", err)
	}
	key, err := parsePrivateKeyPEM(caKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse issuer private key: %w", err)
	}
	if defaultTTL <= 0 {
		defaultTTL = 30 * 24 * time.Hour
	}
	return &ProbeCertificateIssuer{
		caCert:     cert,
		caKey:      key,
		caCertPEM:  caCertPEM,
		defaultTTL: defaultTTL,
		now:        func() time.Time { return time.Now().UTC() },
	}, nil
}

func NewProbeCertificateIssuerFromMaterial(certPath, certPEM, keyPath, keyPEM string, defaultTTL time.Duration) (*ProbeCertificateIssuer, error) {
	loadedCert, err := loadPEMFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read issuer cert path: %w", err)
	}
	loadedKey, err := loadPEMFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read issuer key path: %w", err)
	}
	if strings.TrimSpace(certPEM) != "" {
		loadedCert = certPEM
	}
	if strings.TrimSpace(keyPEM) != "" {
		loadedKey = keyPEM
	}
	if strings.TrimSpace(loadedCert) == "" || strings.TrimSpace(loadedKey) == "" {
		return nil, fmt.Errorf("issuer cert/key material not configured")
	}
	return NewProbeCertificateIssuer(loadedCert, loadedKey, defaultTTL)
}

func (i *ProbeCertificateIssuer) Issue(request ProbeCertificateIssueRequest) (*ProbeCertificateIssueResult, *x509.Certificate, error) {
	if i == nil || i.caCert == nil || i.caKey == nil {
		return nil, nil, fmt.Errorf("certificate issuer is not configured")
	}
	probeID := strings.TrimSpace(request.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe id is required")
	}
	commonName := strings.TrimSpace(request.CommonName)
	if commonName == "" {
		commonName = probeID
	}
	validFor := request.ValidFor
	if validFor <= 0 {
		validFor = i.defaultTTL
	}
	if validFor <= 0 {
		validFor = 30 * 24 * time.Hour
	}
	if validFor > 365*24*time.Hour {
		validFor = 365 * 24 * time.Hour
	}

	now := i.now()
	notBefore := now.Add(-5 * time.Minute)
	notAfter := now.Add(validFor)
	serial, err := randomSerialNumber()
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial number: %w", err)
	}

	uri, _ := url.Parse("spiffe://legator/probe/" + probeID)
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"Legator Probe"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{probeID},
		URIs:                  []*url.URL{uri},
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate client key: %w", err)
	}

	raw, err := x509.CreateCertificate(rand.Reader, tpl, i.caCert, pub, i.caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("issue certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse issued certificate: %w", err)
	}

	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))

	rotateAfter := cert.NotAfter.Add(-validFor / 5)
	if rotateAfter.Before(now) {
		rotateAfter = now
	}

	result := &ProbeCertificateIssueResult{
		CertificatePEM:       certPEM,
		PrivateKeyPEM:        keyPEM,
		CACertificatePEM:     i.caCertPEM,
		FingerprintSHA256:    CertificateFingerprintSHA256(cert),
		SerialNumber:         cert.SerialNumber.Text(16),
		NotBefore:            cert.NotBefore.UTC(),
		NotAfter:             cert.NotAfter.UTC(),
		SuggestedRotateAfter: rotateAfter.UTC(),
	}
	return result, cert, nil
}

func randomSerialNumber() (*big.Int, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialLimit)
}

func parseCertificatePEM(input string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(input))
	if block == nil {
		return nil, fmt.Errorf("no certificate PEM block found")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

func parsePrivateKeyPEM(input string) (crypto.Signer, error) {
	for {
		block, rest := pem.Decode([]byte(input))
		if block == nil {
			return nil, fmt.Errorf("no private key PEM block found")
		}
		switch block.Type {
		case "PRIVATE KEY":
			key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			signer, ok := key.(crypto.Signer)
			if !ok {
				return nil, fmt.Errorf("unsupported private key type %T", key)
			}
			return signer, nil
		case "RSA PRIVATE KEY":
			key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			return key, nil
		case "EC PRIVATE KEY":
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			return key, nil
		default:
			_ = rest
		}
		input = string(rest)
		if strings.TrimSpace(input) == "" {
			break
		}
	}
	return nil, fmt.Errorf("no supported private key PEM block found")
}

func MarshalRSAPrivateKeyPEM(key *rsa.PrivateKey) string {
	if key == nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}
