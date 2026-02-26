// Package signing provides HMAC-SHA256 command signing and verification.
// Every command from the control plane is signed; the probe verifies the
// signature before executing. This prevents MITM command injection.
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Signer creates and verifies HMAC-SHA256 signatures.
type Signer struct {
	key []byte
}

// NewSigner creates a signer with the given shared secret.
func NewSigner(key []byte) *Signer {
	return &Signer{key: key}
}

// Sign computes HMAC-SHA256 over requestID|json(payload).
func (s *Signer) Sign(requestID string, payload any) (string, error) {
	canonical, err := canonicalize(requestID, payload)
	if err != nil {
		return "", fmt.Errorf("canonicalize: %w", err)
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Verify checks a signature matches the payload.
func (s *Signer) Verify(requestID string, payload any, signature string) error {
	expected, err := s.Sign(requestID, payload)
	if err != nil {
		return fmt.Errorf("compute expected: %w", err)
	}
	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	expectedBytes, err := hex.DecodeString(expected)
	if err != nil {
		return fmt.Errorf("decode expected: %w", err)
	}
	if !hmac.Equal(sigBytes, expectedBytes) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

func canonicalize(requestID string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	canonical := make([]byte, 0, len(requestID)+1+len(data))
	canonical = append(canonical, []byte(requestID)...)
	canonical = append(canonical, '|')
	canonical = append(canonical, data...)
	return canonical, nil
}

// DeriveProbeKey derives a per-probe signing key from a master key.
func DeriveProbeKey(masterKey []byte, probeID string) []byte {
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("legator-probe-signing|" + probeID))
	return mac.Sum(nil)
}
