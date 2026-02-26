package alerts

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func newTestEngine(t *testing.T) (*Engine, *Store, *fleet.Manager) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	mgr := fleet.NewManager(zap.NewNop())
	engine := NewEngine(store, mgr, nil, nil, zap.NewNop())
	return engine, store, mgr
}

func TestEvaluate_ProbeOfflineFires(t *testing.T) {
	engine, store, mgr := newTestEngine(t)
	defer func() { _ = store.Close() }()

	rule, err := store.CreateRule(AlertRule{
		Name:    "probe offline",
		Enabled: true,
		Condition: AlertCondition{
			Type:     "probe_offline",
			Duration: "2m",
		},
	})
	if err != nil {
		t.Fatalf("CreateRule error: %v", err)
	}

	probe := mgr.Register("probe-1", "host-1", "linux", "amd64")
	probe.Status = "offline"
	probe.LastSeen = time.Now().UTC().Add(-3 * time.Minute)

	if err := engine.Evaluate(); err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}

	active := store.ActiveAlerts()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}
	if active[0].RuleID != rule.ID {
		t.Fatalf("expected rule id %s, got %s", rule.ID, active[0].RuleID)
	}
	if active[0].Status != "firing" {
		t.Fatalf("expected firing status, got %s", active[0].Status)
	}
}

func TestEvaluate_DiskThresholdFires(t *testing.T) {
	engine, store, mgr := newTestEngine(t)
	defer func() { _ = store.Close() }()

	_, err := store.CreateRule(AlertRule{
		Name:    "disk high",
		Enabled: true,
		Condition: AlertCondition{
			Type:      "disk_threshold",
			Threshold: 80,
		},
	})
	if err != nil {
		t.Fatalf("CreateRule error: %v", err)
	}

	mgr.Register("probe-1", "host-1", "linux", "amd64")
	if err := mgr.Heartbeat("probe-1", &protocol.HeartbeatPayload{
		ProbeID:   "probe-1",
		DiskUsed:  95,
		DiskTotal: 100,
	}); err != nil {
		t.Fatalf("Heartbeat error: %v", err)
	}

	if err := engine.Evaluate(); err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}

	active := store.ActiveAlerts()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}
	if active[0].Status != "firing" {
		t.Fatalf("expected firing status, got %s", active[0].Status)
	}
}

func TestEvaluate_DeduplicatesFiringAlerts(t *testing.T) {
	engine, store, mgr := newTestEngine(t)
	defer func() { _ = store.Close() }()

	rule, err := store.CreateRule(AlertRule{
		Name:    "probe offline",
		Enabled: true,
		Condition: AlertCondition{
			Type:     "probe_offline",
			Duration: "0s",
		},
	})
	if err != nil {
		t.Fatalf("CreateRule error: %v", err)
	}

	probe := mgr.Register("probe-1", "host-1", "linux", "amd64")
	probe.Status = "offline"
	probe.LastSeen = time.Now().UTC().Add(-5 * time.Minute)

	if err := engine.Evaluate(); err != nil {
		t.Fatalf("first Evaluate error: %v", err)
	}
	if err := engine.Evaluate(); err != nil {
		t.Fatalf("second Evaluate error: %v", err)
	}

	history := store.ListEvents(rule.ID, 10)
	if len(history) != 1 {
		t.Fatalf("expected 1 event due to dedupe, got %d", len(history))
	}
	if got := engine.SnapshotFiring(); len(got) != 1 {
		t.Fatalf("expected 1 in-memory firing alert, got %d", len(got))
	}
}

func TestEvaluate_ResolvesWhenConditionClears(t *testing.T) {
	engine, store, mgr := newTestEngine(t)
	defer func() { _ = store.Close() }()

	rule, err := store.CreateRule(AlertRule{
		Name:    "probe offline",
		Enabled: true,
		Condition: AlertCondition{
			Type:     "probe_offline",
			Duration: "1m",
		},
	})
	if err != nil {
		t.Fatalf("CreateRule error: %v", err)
	}

	probe := mgr.Register("probe-1", "host-1", "linux", "amd64")
	probe.Status = "offline"
	probe.LastSeen = time.Now().UTC().Add(-2 * time.Minute)

	if err := engine.Evaluate(); err != nil {
		t.Fatalf("first Evaluate error: %v", err)
	}
	if len(store.ActiveAlerts()) != 1 {
		t.Fatalf("expected one active alert after firing")
	}

	probe.Status = "online"
	probe.LastSeen = time.Now().UTC()

	if err := engine.Evaluate(); err != nil {
		t.Fatalf("second Evaluate error: %v", err)
	}

	if got := store.ActiveAlerts(); len(got) != 0 {
		t.Fatalf("expected no active alerts after resolution, got %d", len(got))
	}

	history := store.ListEvents(rule.ID, 10)
	if len(history) != 1 {
		t.Fatalf("expected 1 upserted event, got %d", len(history))
	}
	if history[0].Status != "resolved" {
		t.Fatalf("expected resolved status, got %s", history[0].Status)
	}
	if history[0].ResolvedAt == nil {
		t.Fatal("expected resolved_at to be set")
	}
}
