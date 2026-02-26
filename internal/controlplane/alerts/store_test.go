package alerts

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRuleCRUDAndEvents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "alerts.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer func() { _ = store.Close() }()

	created, err := store.CreateRule(AlertRule{
		Name:        "Offline probe",
		Description: "fires when probe offline",
		Enabled:     true,
		Condition: AlertCondition{
			Type:     "probe_offline",
			Duration: "2m",
		},
		Actions: []AlertAction{{Type: "webhook", WebhookID: "wh-1"}},
	})
	if err != nil {
		t.Fatalf("CreateRule error: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected created ID")
	}

	fetched, err := store.GetRule(created.ID)
	if err != nil {
		t.Fatalf("GetRule error: %v", err)
	}
	if fetched.Name != created.Name {
		t.Fatalf("expected name %q, got %q", created.Name, fetched.Name)
	}
	if fetched.Condition.Type != "probe_offline" {
		t.Fatalf("unexpected condition type: %s", fetched.Condition.Type)
	}

	rules, err := store.ListRules()
	if err != nil {
		t.Fatalf("ListRules error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	updatedInput := *fetched
	updatedInput.Name = "Offline probe updated"
	updatedInput.Enabled = false
	updatedInput.Condition.Duration = "5m"
	updated, err := store.UpdateRule(updatedInput)
	if err != nil {
		t.Fatalf("UpdateRule error: %v", err)
	}
	if updated.Name != "Offline probe updated" {
		t.Fatalf("expected updated name, got %q", updated.Name)
	}
	if updated.Enabled {
		t.Fatal("expected rule to be disabled")
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Fatal("expected updated_at to be after created_at")
	}

	firing := AlertEvent{
		ID:       "evt-1",
		RuleID:   updated.ID,
		RuleName: updated.Name,
		ProbeID:  "probe-1",
		Status:   "firing",
		Message:  "probe offline",
		FiredAt:  time.Now().UTC(),
	}
	if err := store.RecordEvent(firing); err != nil {
		t.Fatalf("RecordEvent(firing) error: %v", err)
	}

	history := store.ListEvents(updated.ID, 10)
	if len(history) != 1 {
		t.Fatalf("expected 1 event in history, got %d", len(history))
	}
	if history[0].Status != "firing" {
		t.Fatalf("expected firing status, got %s", history[0].Status)
	}

	active := store.ActiveAlerts()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}

	resolvedAt := time.Now().UTC()
	firing.Status = "resolved"
	firing.ResolvedAt = &resolvedAt
	firing.Message = "probe recovered"
	if err := store.RecordEvent(firing); err != nil {
		t.Fatalf("RecordEvent(resolved) error: %v", err)
	}

	history = store.ListEvents(updated.ID, 10)
	if len(history) != 1 {
		t.Fatalf("expected 1 event after upsert, got %d", len(history))
	}
	if history[0].Status != "resolved" {
		t.Fatalf("expected resolved status, got %s", history[0].Status)
	}
	if history[0].ResolvedAt == nil {
		t.Fatal("expected resolved_at to be set")
	}

	if got := store.ActiveAlerts(); len(got) != 0 {
		t.Fatalf("expected 0 active alerts after resolution, got %d", len(got))
	}

	if err := store.DeleteRule(updated.ID); err != nil {
		t.Fatalf("DeleteRule error: %v", err)
	}
	if _, err := store.GetRule(updated.ID); err == nil {
		t.Fatal("expected GetRule to fail after deletion")
	}
}
