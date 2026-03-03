package audit

import (
	"strings"
	"testing"
	"time"
)

func TestComputeEntryHashDeterministic(t *testing.T) {
	key, err := DecodeChainKey(strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}

	evt := Event{
		ID:          "evt-1",
		Timestamp:   time.Date(2026, 3, 3, 6, 0, 0, 0, time.UTC),
		Type:        EventCommandSent,
		ProbeID:     "probe-1",
		WorkspaceID: "ws-a",
		Actor:       "api",
		Summary:     "dispatch ls",
		Detail:      map[string]any{"command": "ls -la"},
	}

	h1, err := ComputeEntryHash("", evt, key)
	if err != nil {
		t.Fatalf("compute hash #1: %v", err)
	}
	h2, err := ComputeEntryHash("", evt, key)
	if err != nil {
		t.Fatalf("compute hash #2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("expected deterministic hash, got %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("expected sha256 hex length 64, got %d", len(h1))
	}
}

func TestComputeEntryHashChangesWithPrevHash(t *testing.T) {
	key, err := DecodeChainKey(strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	evt := Event{ID: "evt-2", Timestamp: time.Now().UTC(), Type: EventCommandResult, Summary: "ok"}

	h1, err := ComputeEntryHash(GenesisHash, evt, key)
	if err != nil {
		t.Fatalf("compute hash #1: %v", err)
	}
	h2, err := ComputeEntryHash(strings.Repeat("c", 64), evt, key)
	if err != nil {
		t.Fatalf("compute hash #2: %v", err)
	}
	if h1 == h2 {
		t.Fatal("expected hash to change when prev hash changes")
	}
}

func TestDecodeChainKeyValidation(t *testing.T) {
	if _, err := DecodeChainKey(""); err == nil {
		t.Fatal("expected error for empty key")
	}
	if _, err := DecodeChainKey("not-hex"); err == nil {
		t.Fatal("expected error for invalid hex")
	}
	if _, err := DecodeChainKey("abcd"); err == nil {
		t.Fatal("expected error for short key")
	}
}
