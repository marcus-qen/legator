package compliance

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// mockFleet implements fleet.Fleet for testing.
type mockFleet struct {
	probes []*fleet.ProbeState
}

func (m *mockFleet) List() []*fleet.ProbeState                    { return m.probes }
func (m *mockFleet) Register(_, _, _, _ string) *fleet.ProbeState { return nil }
func (m *mockFleet) RegisterRemote(_ fleet.RemoteProbeRegistration) (*fleet.ProbeState, error) {
	return nil, nil
}
func (m *mockFleet) Heartbeat(_ string, _ *protocol.HeartbeatPayload) error       { return nil }
func (m *mockFleet) UpdateInventory(_ string, _ *protocol.InventoryPayload) error { return nil }
func (m *mockFleet) Get(_ string) (*fleet.ProbeState, bool)                       { return nil, false }
func (m *mockFleet) FindByHostname(_ string) (*fleet.ProbeState, bool)            { return nil, false }
func (m *mockFleet) ListRemote() []*fleet.ProbeState                              { return nil }
func (m *mockFleet) Inventory(_ fleet.InventoryFilter) fleet.FleetInventory {
	return fleet.FleetInventory{}
}
func (m *mockFleet) SetPolicy(_ string, _ protocol.CapabilityLevel) error { return nil }
func (m *mockFleet) SetAPIKey(_, _ string) error                          { return nil }
func (m *mockFleet) SetStatus(_, _ string) error                          { return nil }
func (m *mockFleet) MarkOffline(_ time.Duration)                          {}
func (m *mockFleet) SetOnline(_ string) error                             { return nil }
func (m *mockFleet) Count() map[string]int                                { return nil }
func (m *mockFleet) SetTags(_ string, _ []string) error                   { return nil }
func (m *mockFleet) ListByTag(_ string) []*fleet.ProbeState               { return nil }
func (m *mockFleet) TagCounts() map[string]int                            { return nil }
func (m *mockFleet) Delete(_ string) error                                { return nil }
func (m *mockFleet) CleanupOffline(_ time.Duration) []string              { return nil }
func (m *mockFleet) SetTenantID(_, _ string) error                        { return nil }
func (m *mockFleet) ListByTenant(_ string) []*fleet.ProbeState            { return nil }

// Compile-time check.
var _ fleet.Fleet = (*mockFleet)(nil)

func TestScannerCheckSelection(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	sc := NewScanner(nil, nil, store, zap.NewNop())

	checks := sc.Checks()
	if len(checks) == 0 {
		t.Fatal("expected builtin checks, got none")
	}

	for _, c := range checks {
		if c.ID == "" {
			t.Errorf("check missing ID: %+v", c)
		}
		if c.Name == "" {
			t.Errorf("check %s missing Name", c.ID)
		}
		if c.Category == "" {
			t.Errorf("check %s missing Category", c.ID)
		}
		if c.Severity == "" {
			t.Errorf("check %s missing Severity", c.ID)
		}
		if c.CheckFunc == nil {
			t.Errorf("check %s missing CheckFunc", c.ID)
		}
	}
}

func TestScannerWithMockProbe(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	f := &mockFleet{
		probes: []*fleet.ProbeState{
			{ID: "probe-a", Hostname: "host-a", Status: "online", Type: ""},
			{ID: "probe-b", Hostname: "host-b", Status: "offline", Type: ""},
		},
	}

	sc := NewScanner(f, nil, store, zap.NewNop())
	resp := sc.Scan(context.Background(), ScanRequest{})

	if resp.ScanID == "" {
		t.Error("expected non-empty scan ID")
	}

	// Non-remote probes with no executor → all results should be skipped
	for _, r := range resp.Results {
		if r.Status != StatusSkipped && r.Status != StatusUnknown {
			t.Errorf("expected skipped/unknown for non-remote probe, got %s for check %s on %s",
				r.Status, r.CheckID, r.ProbeID)
		}
	}

	// Results should be persisted
	stored, err := store.ListResults(ResultFilter{})
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(stored) == 0 {
		t.Error("expected persisted results in store")
	}
}

func TestScannerWithCustomCheckFunc(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	callCount := 0
	customCheck := ComplianceCheck{
		ID:       "test-check",
		Name:     "Test Check",
		Category: "test",
		Severity: SeverityLow,
		CheckFunc: func(ctx context.Context, exec ProbeExecutor) (string, string, error) {
			callCount++
			return StatusUnknown, "no executor available in test", nil
		},
	}

	f := &mockFleet{
		probes: []*fleet.ProbeState{
			{ID: "p1", Status: "online", Type: "remote"},
		},
	}

	sc := &Scanner{
		fleet:  f,
		store:  store,
		checks: []ComplianceCheck{customCheck},
		logger: zap.NewNop(),
	}

	resp := sc.Scan(context.Background(), ScanRequest{})
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	// Remote probe with nil executor → nil exec → skipped
	if resp.Results[0].Status != StatusSkipped {
		t.Errorf("expected skipped (nil remote config), got %s", resp.Results[0].Status)
	}
}

func TestScannerProbeFilter(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	f := &mockFleet{
		probes: []*fleet.ProbeState{
			{ID: "p1", Status: "online"},
			{ID: "p2", Status: "online"},
			{ID: "p3", Status: "online"},
		},
	}

	sc := NewScanner(f, nil, store, zap.NewNop())

	// Scan only p1 and p2
	resp := sc.Scan(context.Background(), ScanRequest{ProbeIDs: []string{"p1", "p2"}})

	seen := map[string]bool{}
	for _, r := range resp.Results {
		seen[r.ProbeID] = true
	}
	if seen["p3"] {
		t.Error("p3 should not have been scanned")
	}
	if !seen["p1"] || !seen["p2"] {
		t.Error("p1 and p2 should have been scanned")
	}
}

func TestBuildSummary(t *testing.T) {
	results := []ComplianceResult{
		{CheckID: "c1", Category: "ssh", Status: StatusPass},
		{CheckID: "c2", Category: "ssh", Status: StatusFail},
		{CheckID: "c3", Category: "firewall", Status: StatusPass},
		{CheckID: "c4", Category: "firewall", Status: StatusWarning},
		{CheckID: "c5", Category: "accounts", Status: StatusUnknown},
	}

	summary := buildSummary(results, 2)

	if summary.TotalChecks != 5 {
		t.Errorf("expected 5 total, got %d", summary.TotalChecks)
	}
	if summary.Passing != 2 {
		t.Errorf("expected 2 passing, got %d", summary.Passing)
	}
	if summary.Failing != 1 {
		t.Errorf("expected 1 failing, got %d", summary.Failing)
	}
	if summary.Warning != 1 {
		t.Errorf("expected 1 warning, got %d", summary.Warning)
	}
	if summary.Unknown != 1 {
		t.Errorf("expected 1 unknown, got %d", summary.Unknown)
	}
	if summary.TotalProbes != 2 {
		t.Errorf("expected 2 probes, got %d", summary.TotalProbes)
	}

	// Score = 2 / (2+1+1) = 50%
	if summary.ScorePct < 49.9 || summary.ScorePct > 50.1 {
		t.Errorf("expected score ~50%%, got %.1f", summary.ScorePct)
	}

	if _, ok := summary.ByCategory["ssh"]; !ok {
		t.Error("expected ssh in ByCategory")
	}
	if _, ok := summary.ByCategory["firewall"]; !ok {
		t.Error("expected firewall in ByCategory")
	}
}

// nopSender satisfies the unexported commandSender interface in commanddispatch.
type nopSender struct{}

func (n *nopSender) SendTo(_ string, _ protocol.MessageType, _ any) error { return nil }

// nopTracker satisfies the unexported commandTracker interface in commanddispatch.
type nopTracker struct{}

func (n *nopTracker) Track(requestID, _ /*probeID*/, _ /*command*/ string, _ protocol.CapabilityLevel) *cmdtracker.PendingCommand {
	return &cmdtracker.PendingCommand{
		RequestID: requestID,
		Result:    make(chan *protocol.CommandResultPayload, 1),
	}
}

func (n *nopTracker) Cancel(_ string) {}

// TestBuildExecutor verifies that buildExecutor returns a non-nil executor for an
// online agent-type probe when commandDispatch is properly wired (non-nil hub).
func TestBuildExecutor(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Wire up dispatch with real (non-nil) sender/tracker — simulates post-hub init state.
	dispatch := corecommanddispatch.NewService(&nopSender{}, &nopTracker{})

	sc := NewScannerWithCommandDispatch(nil, nil, store, zap.NewNop(), dispatch)

	t.Run("online agent probe returns non-nil executor", func(t *testing.T) {
		ps := &fleet.ProbeState{
			ID:       "agent-1",
			Hostname: "host-agent",
			Status:   "online",
			Type:     fleet.ProbeTypeAgent,
		}
		exec := sc.buildExecutor(context.Background(), ps)
		if exec == nil {
			t.Fatal("expected non-nil ProbeExecutor for online agent probe with wired dispatch, got nil (all checks would be skipped)")
		}
	})

	t.Run("agent probe type with whitespace and mixed case returns non-nil executor", func(t *testing.T) {
		ps := &fleet.ProbeState{
			ID:       "agent-1b",
			Hostname: "host-agent-1b",
			Status:   "online",
			Type:     "  AgEnT  ",
		}
		exec := sc.buildExecutor(context.Background(), ps)
		if exec == nil {
			t.Fatal("expected non-nil ProbeExecutor for normalized agent probe type, got nil")
		}
	})

	t.Run("empty probe type defaults to agent and returns non-nil executor", func(t *testing.T) {
		ps := &fleet.ProbeState{
			ID:       "agent-legacy",
			Hostname: "host-agent-legacy",
			Status:   "online",
			Type:     "",
		}
		exec := sc.buildExecutor(context.Background(), ps)
		if exec == nil {
			t.Fatal("expected non-nil ProbeExecutor for legacy empty probe type, got nil")
		}
	})

	t.Run("offline agent probe returns nil executor", func(t *testing.T) {
		ps := &fleet.ProbeState{
			ID:       "agent-2",
			Hostname: "host-agent-2",
			Status:   "offline",
			Type:     fleet.ProbeTypeAgent,
		}
		exec := sc.buildExecutor(context.Background(), ps)
		if exec != nil {
			t.Fatal("expected nil ProbeExecutor for offline agent probe, got non-nil")
		}
	})

	t.Run("nil dispatch returns nil executor for agent probe", func(t *testing.T) {
		scNilDispatch := NewScannerWithCommandDispatch(nil, nil, store, zap.NewNop(), nil)
		ps := &fleet.ProbeState{
			ID:     "agent-3",
			Status: "online",
			Type:   fleet.ProbeTypeAgent,
		}
		exec := scNilDispatch.buildExecutor(context.Background(), ps)
		if exec != nil {
			t.Fatal("expected nil ProbeExecutor when dispatch is nil, got non-nil")
		}
	})
}

func TestScanOnlineAgentProbeTypeVariantsAreNotSkipped(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	dispatch := corecommanddispatch.NewService(&nopSender{}, &nopTracker{})

	check := ComplianceCheck{
		ID:       "agent-exec-path",
		Name:     "Agent executor path",
		Category: "test",
		Severity: SeverityLow,
		CheckFunc: func(_ context.Context, _ ProbeExecutor) (string, string, error) {
			return StatusPass, "executor is available", nil
		},
	}

	testCases := []struct {
		name string
		typ  string
	}{
		{name: "canonical agent type", typ: fleet.ProbeTypeAgent},
		{name: "agent type with whitespace and mixed case", typ: "  AgEnT  "},
		{name: "legacy empty type", typ: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			f := &mockFleet{probes: []*fleet.ProbeState{{
				ID:       "agent-probe",
				Hostname: "agent-host",
				Status:   "online",
				Type:     tc.typ,
			}}}

			sc := NewScannerWithCommandDispatch(f, nil, store, zap.NewNop(), dispatch)
			sc.checks = []ComplianceCheck{check}

			resp := sc.Scan(context.Background(), ScanRequest{})
			if len(resp.Results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(resp.Results))
			}
			if resp.Results[0].Status == StatusSkipped {
				t.Fatalf("expected non-skipped result for type %q, got skipped", tc.typ)
			}
		})
	}
}
