package connection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func TestClientTypeAndDefaults(t *testing.T) {
	const (
		expectedServer = "https://control.example/ws"
		expectedProbe  = "probe-conn"
		expectedAPIKey = "api-key-abc"
	)

	c := NewClient(expectedServer, expectedProbe, expectedAPIKey, zap.NewNop())
	if c == nil {
		t.Fatal("expected non-nil client")
	}

	if c.serverURL != expectedServer {
		t.Fatalf("expected serverURL %q, got %q", expectedServer, c.serverURL)
	}
	if c.probeID != expectedProbe {
		t.Fatalf("expected probeID %q, got %q", expectedProbe, c.probeID)
	}
	if c.apiKey != expectedAPIKey {
		t.Fatalf("expected apiKey %q, got %q", expectedAPIKey, c.apiKey)
	}

	if c.Inbox() == nil {
		t.Fatal("expected non-nil inbox channel")
	}
	if cap(c.inbox) != 64 {
		t.Fatalf("expected inbox capacity %d, got %d", 64, cap(c.inbox))
	}

	if err := c.Send(protocol.MsgPing, map[string]string{"x": "y"}); err == nil {
		t.Fatal("expected Send to return error when not connected")
	}
}

func TestClientTimingConstants(t *testing.T) {
	if heartbeatInterval != 30*time.Second {
		t.Fatalf("expected heartbeatInterval %s, got %s", 30*time.Second, heartbeatInterval)
	}
	if offlineThreshold != 60*time.Second {
		t.Fatalf("expected offlineThreshold %s, got %s", 60*time.Second, offlineThreshold)
	}
	if maxReconnectDelay != 5*time.Minute {
		t.Fatalf("expected maxReconnectDelay %s, got %s", 5*time.Minute, maxReconnectDelay)
	}
	if writeTimeout != 10*time.Second {
		t.Fatalf("expected writeTimeout %s, got %s", 10*time.Second, writeTimeout)
	}
	if pongWait != 70*time.Second {
		t.Fatalf("expected pongWait %s, got %s", 70*time.Second, pongWait)
	}
}

func TestEnvelopeJSONSerialization(t *testing.T) {
	now := time.Date(2026, 2, 25, 23, 0, 0, 0, time.UTC)

	original := protocol.Envelope{
		ID:        "env-conn-123",
		Type:      protocol.MsgCommandResult,
		Timestamp: now,
		Payload: protocol.CommandResultPayload{
			RequestID: "req-456",
			ExitCode:  0,
			Stdout:    "ok",
			Stderr:    "",
			Duration:  123,
		},
		Signature: "sig-conn",
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var decoded protocol.Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if decoded.ID != original.ID {
		t.Fatalf("ID mismatch: got %q want %q", decoded.ID, original.ID)
	}
	if decoded.Type != original.Type {
		t.Fatalf("Type mismatch: got %q want %q", decoded.Type, original.Type)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Fatalf("Timestamp mismatch: got %v want %v", decoded.Timestamp, original.Timestamp)
	}
	if decoded.Signature != original.Signature {
		t.Fatalf("Signature mismatch: got %q want %q", decoded.Signature, original.Signature)
	}

	decodedPayloadBytes, err := json.Marshal(decoded.Payload)
	if err != nil {
		t.Fatalf("marshal decoded payload: %v", err)
	}

	var decodedPayload protocol.CommandResultPayload
	if err := json.Unmarshal(decodedPayloadBytes, &decodedPayload); err != nil {
		t.Fatalf("unmarshal decoded payload: %v", err)
	}

	if decodedPayload.RequestID != original.Payload.(protocol.CommandResultPayload).RequestID {
		t.Fatalf("Payload.RequestID mismatch: got %q want %q", decodedPayload.RequestID, original.Payload.(protocol.CommandResultPayload).RequestID)
	}
}
