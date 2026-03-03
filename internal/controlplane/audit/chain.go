package audit

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// GenesisHash anchors the first signed audit event in a chain.
	GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

	chainAlgorithm = "hmac-sha256"
)

// ChainAlgorithm returns the algorithm label used for audit hash-chain entries.
func ChainAlgorithm() string {
	return chainAlgorithm
}

// GenerateChainKeyHex generates a random 32-byte key encoded as hex.
func GenerateChainKeyHex() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate chain key: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// DecodeChainKey decodes a hex-encoded HMAC key and validates minimum strength.
func DecodeChainKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("chain key is required")
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode chain key: %w", err)
	}
	if len(decoded) < 32 {
		return nil, fmt.Errorf("chain key must be >= 64 hex chars (32 bytes)")
	}
	return decoded, nil
}

// ComputeEntryHash computes the signed hash for a single audit event.
//
// Hash input = prev_hash + "\n" + canonical_json(event_without_hash_fields).
func ComputeEntryHash(prevHash string, evt Event, key []byte) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("chain key is required")
	}

	payload, err := canonicalEntryPayload(evt)
	if err != nil {
		return "", err
	}

	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(normalizePrevHash(prevHash)))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func normalizePrevHash(prevHash string) string {
	prevHash = strings.TrimSpace(prevHash)
	if prevHash == "" {
		return GenesisHash
	}
	return prevHash
}

type entryPayload struct {
	ID          string `json:"id"`
	Timestamp   string `json:"timestamp"`
	Type        string `json:"type"`
	ProbeID     string `json:"probe_id"`
	WorkspaceID string `json:"workspace_id"`
	Actor       string `json:"actor"`
	Summary     string `json:"summary"`
	Detail      any    `json:"detail"`
	Before      any    `json:"before"`
	After       any    `json:"after"`
}

func canonicalEntryPayload(evt Event) ([]byte, error) {
	ts := ""
	if !evt.Timestamp.IsZero() {
		ts = evt.Timestamp.UTC().Format(time.RFC3339Nano)
	}

	payload := entryPayload{
		ID:          strings.TrimSpace(evt.ID),
		Timestamp:   ts,
		Type:        string(evt.Type),
		ProbeID:     strings.TrimSpace(evt.ProbeID),
		WorkspaceID: strings.TrimSpace(evt.WorkspaceID),
		Actor:       strings.TrimSpace(evt.Actor),
		Summary:     evt.Summary,
		Detail:      evt.Detail,
		Before:      evt.Before,
		After:       evt.After,
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal entry payload: %w", err)
	}
	return out, nil
}
