package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func testCA(t *testing.T) (certPEM, keyPEM string, cert *x509.Certificate, key *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	now := time.Now().UTC()
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "legator-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	raw, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	cert, err = x509.ParseCertificate(raw)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
	keyPEM = MarshalRSAPrivateKeyPEM(key)
	return certPEM, keyPEM, cert, key
}

func issueLeaf(t *testing.T, ca *x509.Certificate, caKey *rsa.PrivateKey, cn string, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	raw, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	return cert
}

func TestProbeCertificateRegistryRotationOverlap(t *testing.T) {
	_, _, caCert, caKey := testCA(t)
	now := time.Now().UTC()
	leafA := issueLeaf(t, caCert, caKey, "probe-1", now.Add(-time.Hour), now.Add(4*time.Hour))
	leafB := issueLeaf(t, caCert, caKey, "probe-1-next", now.Add(-30*time.Minute), now.Add(8*time.Hour))

	registry := NewProbeCertificateRegistry()
	recA, err := registry.Register("probe-1", "manual", leafA)
	if err != nil {
		t.Fatalf("register cert A: %v", err)
	}
	recB, err := registry.Register("probe-1", "issued", leafB)
	if err != nil {
		t.Fatalf("register cert B: %v", err)
	}
	if recA.FingerprintSHA256 == recB.FingerprintSHA256 {
		t.Fatal("expected different fingerprints for overlap test")
	}

	if _, ok := registry.Match("probe-1", leafA, now); !ok {
		t.Fatal("expected cert A to be active")
	}
	if _, ok := registry.Match("probe-1", leafB, now); !ok {
		t.Fatal("expected cert B to be active")
	}

	listed := registry.List("probe-1")
	if len(listed) != 2 {
		t.Fatalf("expected 2 registered certs, got %d", len(listed))
	}

	if !registry.Revoke("probe-1", recA.FingerprintSHA256) {
		t.Fatal("expected revoke to succeed")
	}
	if _, ok := registry.Match("probe-1", leafA, now.Add(time.Minute)); ok {
		t.Fatal("expected revoked cert A to be inactive")
	}
	if _, ok := registry.Match("probe-1", leafB, now.Add(time.Minute)); !ok {
		t.Fatal("expected cert B to stay active after overlap revoke")
	}
}

func TestProbeAuthenticatorModes(t *testing.T) {
	_, _, caCert, caKey := testCA(t)
	now := time.Now().UTC()
	leafValid := issueLeaf(t, caCert, caKey, "probe-valid", now.Add(-time.Hour), now.Add(24*time.Hour))
	leafExpired := issueLeaf(t, caCert, caKey, "probe-expired", now.Add(-48*time.Hour), now.Add(-24*time.Hour))

	verifier, err := NewProbeClientCertVerifier("", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})))
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	registry := NewProbeCertificateRegistry()
	if _, err := registry.Register("probe-1", "manual", leafValid); err != nil {
		t.Fatalf("register valid cert: %v", err)
	}

	required := NewProbeAuthenticator(ProbeAuthenticatorConfig{
		Mode:         ProbeAuthModeRequired,
		CertVerifier: verifier,
		CertRegistry: registry,
		VerifyAPIKey: func(probeID, token string) bool { return token == "api-ok" },
	})

	outcome := required.Authenticate("probe-1", "", nil)
	if outcome.Allowed || outcome.Reason != "missing_client_certificate" {
		t.Fatalf("expected missing cert rejection, got %+v", outcome)
	}

	outcome = required.Authenticate("probe-1", "", &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafValid, caCert}})
	if !outcome.Allowed || outcome.Method != ProbeAuthMethodMTLS {
		t.Fatalf("expected mTLS success, got %+v", outcome)
	}

	leafUnregistered := issueLeaf(t, caCert, caKey, "probe-unregistered", now.Add(-time.Hour), now.Add(24*time.Hour))
	outcome = required.Authenticate("probe-1", "", &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafUnregistered, caCert}})
	if outcome.Allowed || outcome.Reason != "certificate_not_registered" {
		t.Fatalf("expected unregistered cert rejection, got %+v", outcome)
	}

	outcome = required.Authenticate("probe-1", "", &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafExpired, caCert}})
	if outcome.Allowed || outcome.Reason != "certificate_expired" {
		t.Fatalf("expected expired cert rejection, got %+v", outcome)
	}

	off := NewProbeAuthenticator(ProbeAuthenticatorConfig{
		Mode:         ProbeAuthModeOff,
		VerifyAPIKey: func(_ string, token string) bool { return token == "api-ok" },
	})
	outcome = off.Authenticate("probe-1", "api-ok", nil)
	if !outcome.Allowed || outcome.Method != ProbeAuthMethodAPIKey {
		t.Fatalf("expected API key auth success in off mode, got %+v", outcome)
	}

	optional := NewProbeAuthenticator(ProbeAuthenticatorConfig{
		Mode:         ProbeAuthModeOptional,
		CertVerifier: verifier,
		CertRegistry: registry,
		VerifyAPIKey: func(_ string, token string) bool { return token == "api-ok" },
	})
	outcome = optional.Authenticate("probe-1", "api-ok", nil)
	if !outcome.Allowed || outcome.Method != ProbeAuthMethodAPIKey {
		t.Fatalf("expected optional mode fallback to API key, got %+v", outcome)
	}
}

func TestProbeCertificateIssuerIssuesClientCertificate(t *testing.T) {
	caCertPEM, caKeyPEM, _, _ := testCA(t)
	issuer, err := NewProbeCertificateIssuer(caCertPEM, caKeyPEM, 12*time.Hour)
	if err != nil {
		t.Fatalf("new issuer: %v", err)
	}

	issued, cert, err := issuer.Issue(ProbeCertificateIssueRequest{ProbeID: "probe-issue", ValidFor: 6 * time.Hour})
	if err != nil {
		t.Fatalf("issue certificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected parsed certificate")
	}
	if issued.FingerprintSHA256 == "" || issued.CertificatePEM == "" || issued.PrivateKeyPEM == "" {
		t.Fatalf("unexpected issue result: %+v", issued)
	}
	if issued.NotAfter.Sub(issued.NotBefore) <= 0 {
		t.Fatalf("invalid cert validity window: %+v", issued)
	}
}
