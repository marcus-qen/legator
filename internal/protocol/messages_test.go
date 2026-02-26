package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

type envelopePayloadStub struct {
	ProbeID string `json:"probe_id"`
	Count   int    `json:"count"`
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 2, 25, 23, 0, 0, 0, time.UTC)
	original := Envelope{
		ID:        "env-123",
		Type:      MsgInventory,
		Timestamp: now,
		Payload: envelopePayloadStub{
			ProbeID: "probe-abc",
			Count:   2,
		},
		Signature: "sig",
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var decoded Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %q want %q", decoded.ID, original.ID)
	}
	if decoded.Type != original.Type {
		t.Errorf("Type mismatch: got %q want %q", decoded.Type, original.Type)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v want %v", decoded.Timestamp, original.Timestamp)
	}

	decodedPayload := envelopePayloadStub{}
	payloadBytes, err := json.Marshal(decoded.Payload)
	if err != nil {
		t.Fatalf("marshal decoded payload: %v", err)
	}
	if err := json.Unmarshal(payloadBytes, &decodedPayload); err != nil {
		t.Fatalf("unmarshal decoded payload: %v", err)
	}

	if decodedPayload != original.Payload {
		t.Errorf("payload mismatch: got %+v want %+v", decodedPayload, original.Payload)
	}

	if decoded.Signature != original.Signature {
		t.Errorf("signature mismatch: got %q want %q", decoded.Signature, original.Signature)
	}
}

func TestMessageTypeConstants(t *testing.T) {
	tests := []struct {
		name string
		got  MessageType
		want MessageType
	}{
		{"MsgRegister", MsgRegister, "register"},
		{"MsgHeartbeat", MsgHeartbeat, "heartbeat"},
		{"MsgInventory", MsgInventory, "inventory"},
		{"MsgCommandResult", MsgCommandResult, "command_result"},
		{"MsgError", MsgError, "error"},
		{"MsgRegistered", MsgRegistered, "registered"},
		{"MsgCommand", MsgCommand, "command"},
		{"MsgPolicyUpdate", MsgPolicyUpdate, "policy_update"},
		{"MsgPing", MsgPing, "ping"},
		{"MsgPong", MsgPong, "pong"},
		{"MsgUpdate", MsgUpdate, "update"},
		{"MsgKeyRotation", MsgKeyRotation, "key_rotation"},
		{"MsgOutputChunk", MsgOutputChunk, "output_chunk"},
	}

	seen := map[string]struct{}{}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
		if _, ok := seen[string(tc.got)]; ok {
			t.Fatalf("duplicate MessageType value detected: %q", tc.got)
		}
		seen[string(tc.got)] = struct{}{}
	}

	if len(seen) != len(tests) {
		t.Errorf("expected %d unique message types, got %d", len(tests), len(seen))
	}
}

func TestCapabilityLevelValues(t *testing.T) {
	tests := []struct {
		name string
		got  CapabilityLevel
		want CapabilityLevel
	}{
		{"CapObserve", CapObserve, "observe"},
		{"CapDiagnose", CapDiagnose, "diagnose"},
		{"CapRemediate", CapRemediate, "remediate"},
	}

	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestKeyRotationPayloadJSON(t *testing.T) {
	payload := KeyRotationPayload{
		NewKey:    "lgk_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd",
		ExpiresAt: "2026-02-27T02:00:00Z",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal key rotation payload: %v", err)
	}

	var decoded KeyRotationPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal key rotation payload: %v", err)
	}

	if decoded.NewKey != payload.NewKey {
		t.Fatalf("new key mismatch: got %q want %q", decoded.NewKey, payload.NewKey)
	}
	if decoded.ExpiresAt != payload.ExpiresAt {
		t.Fatalf("expires_at mismatch: got %q want %q", decoded.ExpiresAt, payload.ExpiresAt)
	}
}
