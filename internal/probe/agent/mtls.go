package agent

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

func materialOrFile(path, inline string) (string, error) {
	if strings.TrimSpace(inline) != "" {
		return inline, nil
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	data, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func buildMTLSDialer(cfg MTLSConfig) (*websocket.Dialer, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	certPEM, err := materialOrFile(cfg.ClientCertPath, cfg.ClientCertPEM)
	if err != nil {
		return nil, fmt.Errorf("read mtls client cert: %w", err)
	}
	keyPEM, err := materialOrFile(cfg.ClientKeyPath, cfg.ClientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("read mtls client key: %w", err)
	}
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(keyPEM) == "" {
		return nil, fmt.Errorf("mtls enabled but client cert/key not configured")
	}

	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse mtls client keypair: %w", err)
	}

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
	}

	rootCAPEM, err := materialOrFile(cfg.RootCAPath, cfg.RootCAPEM)
	if err != nil {
		return nil, fmt.Errorf("read mtls root ca: %w", err)
	}
	if strings.TrimSpace(rootCAPEM) != "" {
		pool := x509.NewCertPool()
		if ok := pool.AppendCertsFromPEM([]byte(rootCAPEM)); !ok {
			return nil, fmt.Errorf("parse mtls root ca PEM")
		}
		tlsCfg.RootCAs = pool
	}

	dialer := *websocket.DefaultDialer
	dialer.TLSClientConfig = tlsCfg
	return &dialer, nil
}
