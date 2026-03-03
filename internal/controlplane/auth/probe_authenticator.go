package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	ProbeAuthModeOff      = "off"
	ProbeAuthModeOptional = "optional"
	ProbeAuthModeRequired = "required"
)

const (
	ProbeAuthMethodAPIKey = "api_key"
	ProbeAuthMethodMTLS   = "mtls"
)

// ProbeAuthOutcome captures probe websocket handshake auth result.
type ProbeAuthOutcome struct {
	Allowed     bool
	Method      string
	StatusCode  int
	Reason      string
	Message     string
	Certificate *ProbeCertificateRecord
}

type ProbeAuthenticator struct {
	mode          string
	certVerifier  *ProbeClientCertVerifier
	certRegistry  *ProbeCertificateRegistry
	verifyAPIKey  func(probeID, token string) bool
	now           func() time.Time
	allowFallback bool
}

func NormalizeProbeAuthMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ProbeAuthModeOptional:
		return ProbeAuthModeOptional
	case ProbeAuthModeRequired:
		return ProbeAuthModeRequired
	default:
		return ProbeAuthModeOff
	}
}

type ProbeAuthenticatorConfig struct {
	Mode             string
	CertVerifier     *ProbeClientCertVerifier
	CertRegistry     *ProbeCertificateRegistry
	VerifyAPIKey     func(probeID, token string) bool
	AllowAPIFallback bool
}

func NewProbeAuthenticator(cfg ProbeAuthenticatorConfig) *ProbeAuthenticator {
	mode := NormalizeProbeAuthMode(cfg.Mode)
	allowFallback := cfg.AllowAPIFallback
	if mode == ProbeAuthModeOff {
		allowFallback = true
	}
	if mode == ProbeAuthModeOptional && !cfg.AllowAPIFallback {
		allowFallback = true
	}
	return &ProbeAuthenticator{
		mode:          mode,
		certVerifier:  cfg.CertVerifier,
		certRegistry:  cfg.CertRegistry,
		verifyAPIKey:  cfg.VerifyAPIKey,
		now:           func() time.Time { return time.Now().UTC() },
		allowFallback: allowFallback,
	}
}

func (a *ProbeAuthenticator) Mode() string {
	if a == nil {
		return ProbeAuthModeOff
	}
	return a.mode
}

func (a *ProbeAuthenticator) Authenticate(probeID, bearerToken string, tlsState *tls.ConnectionState) ProbeAuthOutcome {
	if a == nil {
		return ProbeAuthOutcome{Allowed: false, StatusCode: 403, Reason: "authenticator_unconfigured", Message: "probe authenticator not configured"}
	}
	probeID = strings.TrimSpace(probeID)
	if probeID == "" {
		return ProbeAuthOutcome{Allowed: false, StatusCode: 400, Reason: "missing_probe_id", Message: "missing probe id"}
	}

	now := a.now()
	if a.mode != ProbeAuthModeOff {
		if cert, ok := firstPeerCertificate(tlsState); ok {
			if a.certVerifier == nil {
				return ProbeAuthOutcome{Allowed: false, StatusCode: 500, Method: ProbeAuthMethodMTLS, Reason: "mtls_not_configured", Message: "mTLS is enabled but client CA is not configured"}
			}
			if err := a.certVerifier.Verify(cert, tlsState.PeerCertificates[1:], now); err != nil {
				return ProbeAuthOutcome{
					Allowed:    false,
					StatusCode: 403,
					Method:     ProbeAuthMethodMTLS,
					Reason:     classifyCertError(err),
					Message:    err.Error(),
				}
			}
			if a.certRegistry == nil {
				return ProbeAuthOutcome{Allowed: false, StatusCode: 500, Method: ProbeAuthMethodMTLS, Reason: "cert_registry_unconfigured", Message: "certificate registry is not configured"}
			}
			rec, ok := a.certRegistry.Match(probeID, cert, now)
			if !ok {
				return ProbeAuthOutcome{Allowed: false, StatusCode: 403, Method: ProbeAuthMethodMTLS, Reason: "certificate_not_registered", Message: "certificate fingerprint is not registered for probe"}
			}
			return ProbeAuthOutcome{Allowed: true, Method: ProbeAuthMethodMTLS, Reason: "ok", Message: "probe authenticated with client certificate", Certificate: &rec}
		}

		if a.mode == ProbeAuthModeRequired {
			return ProbeAuthOutcome{Allowed: false, StatusCode: 401, Method: ProbeAuthMethodMTLS, Reason: "missing_client_certificate", Message: "client certificate required"}
		}
	}

	if !a.allowFallback {
		return ProbeAuthOutcome{Allowed: false, StatusCode: 401, Reason: "api_fallback_disabled", Message: "api key fallback disabled"}
	}
	if strings.TrimSpace(bearerToken) == "" {
		return ProbeAuthOutcome{Allowed: false, StatusCode: 401, Method: ProbeAuthMethodAPIKey, Reason: "missing_authorization", Message: "missing authorization"}
	}
	if a.verifyAPIKey == nil || !a.verifyAPIKey(probeID, bearerToken) {
		return ProbeAuthOutcome{Allowed: false, StatusCode: 403, Method: ProbeAuthMethodAPIKey, Reason: "invalid_api_key", Message: "invalid credentials"}
	}
	return ProbeAuthOutcome{Allowed: true, Method: ProbeAuthMethodAPIKey, Reason: "ok", Message: "probe authenticated with api key"}
}

func firstPeerCertificate(state *tls.ConnectionState) (*x509.Certificate, bool) {
	if state == nil || len(state.PeerCertificates) == 0 || state.PeerCertificates[0] == nil {
		return nil, false
	}
	return state.PeerCertificates[0], true
}

// ProbeClientCertVerifier verifies probe mTLS certificates using configured root CAs.
type ProbeClientCertVerifier struct {
	roots *x509.CertPool
}

func NewProbeClientCertVerifier(clientCAPath, clientCAPEM string) (*ProbeClientCertVerifier, error) {
	pool, err := loadClientCAPool(clientCAPath, clientCAPEM)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, fmt.Errorf("no client CA certificates configured")
	}
	return &ProbeClientCertVerifier{roots: pool}, nil
}

func loadClientCAPool(path, inlinePEM string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	addedAny := false

	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(strings.TrimSpace(path))
		if err != nil {
			return nil, fmt.Errorf("read client ca: %w", err)
		}
		if ok := pool.AppendCertsFromPEM(data); !ok {
			return nil, fmt.Errorf("parse client ca PEM from %s", path)
		}
		addedAny = true
	}

	if strings.TrimSpace(inlinePEM) != "" {
		if ok := pool.AppendCertsFromPEM([]byte(inlinePEM)); !ok {
			return nil, fmt.Errorf("parse inline client ca PEM")
		}
		addedAny = true
	}

	if !addedAny {
		return nil, nil
	}
	return pool, nil
}

func (v *ProbeClientCertVerifier) Verify(leaf *x509.Certificate, intermediates []*x509.Certificate, now time.Time) error {
	if v == nil || v.roots == nil {
		return fmt.Errorf("client CA verifier is not configured")
	}
	if leaf == nil {
		return fmt.Errorf("missing client certificate")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	intermediatePool := x509.NewCertPool()
	for _, cert := range intermediates {
		if cert != nil {
			intermediatePool.AddCert(cert)
		}
	}

	opts := x509.VerifyOptions{
		CurrentTime:   now,
		Roots:         v.roots,
		Intermediates: intermediatePool,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return err
	}
	return nil
}

func classifyCertError(err error) string {
	if err == nil {
		return "ok"
	}
	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &invalidErr) {
		if invalidErr.Reason == x509.Expired {
			return "certificate_expired"
		}
		return "certificate_invalid"
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return "certificate_invalid"
	}
	var unknownAuthErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthErr) {
		return "certificate_invalid"
	}
	if strings.Contains(strings.ToLower(err.Error()), "expired") {
		return "certificate_expired"
	}
	return "certificate_invalid"
}
