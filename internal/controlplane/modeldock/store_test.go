package modeldock

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "modeldock.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreCreateActivateAndAggregateUsage(t *testing.T) {
	store := newTestStore(t)

	first, err := store.CreateProfile(Profile{
		Name:     "Primary",
		Provider: "openai",
		BaseURL:  "https://api.openai.com/v1",
		Model:    "gpt-4o-mini",
		APIKey:   "sk-first",
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("create first profile: %v", err)
	}

	second, err := store.CreateProfile(Profile{
		Name:     "Secondary",
		Provider: "openai",
		BaseURL:  "https://api.openai.com/v1",
		Model:    "gpt-4.1-mini",
		APIKey:   "sk-second",
	})
	if err != nil {
		t.Fatalf("create second profile: %v", err)
	}

	active, err := store.GetActiveProfile()
	if err != nil {
		t.Fatalf("get active profile: %v", err)
	}
	if active.ID != first.ID {
		t.Fatalf("expected first profile active, got %s", active.ID)
	}

	if _, err := store.ActivateProfile(second.ID); err != nil {
		t.Fatalf("activate second profile: %v", err)
	}

	active, err = store.GetActiveProfile()
	if err != nil {
		t.Fatalf("get active profile after switch: %v", err)
	}
	if active.ID != second.ID {
		t.Fatalf("expected second profile active, got %s", active.ID)
	}

	if err := store.RecordUsage(UsageRecord{
		ProfileID:        second.ID,
		Feature:          FeatureTask,
		PromptTokens:     10,
		CompletionTokens: 6,
		TotalTokens:      16,
		TS:               time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record usage second: %v", err)
	}

	if err := store.RecordUsage(UsageRecord{
		ProfileID:        EnvProfileID,
		Feature:          FeatureProbeChat,
		PromptTokens:     2,
		CompletionTokens: 3,
		TS:               time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record usage env: %v", err)
	}

	items, totals, _, err := store.AggregateUsage(24 * time.Hour)
	if err != nil {
		t.Fatalf("aggregate usage: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 usage groups, got %d", len(items))
	}
	if totals.TotalTokens != 21 {
		t.Fatalf("expected total tokens 21, got %d", totals.TotalTokens)
	}
}

func TestMaskAPIKey(t *testing.T) {
	masked := MaskAPIKey("sk-abcdef123456")
	if masked == "sk-abcdef123456" {
		t.Fatalf("mask should hide key")
	}
	if masked != "sk-...3456" {
		t.Fatalf("unexpected mask output: %s", masked)
	}
}
