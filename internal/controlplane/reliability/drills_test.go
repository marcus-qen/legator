package reliability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Failure-detecting fakes (inject to prove drills catch bad behaviour)
// ---------------------------------------------------------------------------

// brokenProbeRegistry never marks probes as offline — drill should detect this.
type brokenProbeRegistry struct{}

func (b *brokenProbeRegistry) MarkOffline(_ string) error { return nil }
func (b *brokenProbeRegistry) MarkOnline(_ string) error  { return nil }
func (b *brokenProbeRegistry) IsOffline(_ string) bool    { return false }

// alwaysOfflineProbeRegistry stays offline after MarkOnline — drill should detect this.
type alwaysOfflineProbeRegistry struct{}

func (a *alwaysOfflineProbeRegistry) MarkOffline(_ string) error { return nil }
func (a *alwaysOfflineProbeRegistry) MarkOnline(_ string) error  { return nil }
func (a *alwaysOfflineProbeRegistry) IsOffline(_ string) bool    { return true }

// brokenDBWriter whose Write always fails after the first call.
type brokenDBWriter struct {
	writes int32
}

func (b *brokenDBWriter) Write(_ string) error {
	n := atomic.AddInt32(&b.writes, 1)
	if n > 1 {
		return errors.New("simulated write failure: disk full")
	}
	return nil
}
func (b *brokenDBWriter) Read() (string, error) { return "cached-data", nil }

// unresponsiveLLMClient whose Read never returns.
type unresponsiveLLMClient struct{}

func (u *unresponsiveLLMClient) Chat(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", fmt.Errorf("LLM provider timeout: context deadline exceeded")
}

// noBackpressureMQ accepts everything — drill should detect missing backpressure.
type noBackpressureMQ struct {
	mu   sync.Mutex
	msgs []string
}

func (q *noBackpressureMQ) Enqueue(msg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, msg)
	return nil
}
func (q *noBackpressureMQ) QueueDepth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}

// deadlockJobQueue blocks Drain forever — drill should time-out gracefully.
type deadlockJobQueue struct {
	mu   sync.Mutex
	jobs []string
}

func (q *deadlockJobQueue) Submit(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, id)
	return nil
}
func (q *deadlockJobQueue) Drain(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// ---------------------------------------------------------------------------
// DrillDefinition tests
// ---------------------------------------------------------------------------

func TestAllDefinitions_Count(t *testing.T) {
	defs := allDefinitions()
	if len(defs) < 5 {
		t.Errorf("expected at least 5 drill definitions, got %d", len(defs))
	}
}

func TestAllDefinitions_UniqueNames(t *testing.T) {
	defs := allDefinitions()
	seen := make(map[DrillScenario]bool)
	for _, d := range defs {
		if seen[d.Name] {
			t.Errorf("duplicate drill scenario name: %s", d.Name)
		}
		seen[d.Name] = true
	}
}

func TestAllDefinitions_FieldsPopulated(t *testing.T) {
	for _, d := range allDefinitions() {
		if d.Name == "" {
			t.Error("drill definition has empty name")
		}
		if d.Title == "" {
			t.Errorf("drill %s has empty title", d.Name)
		}
		if d.Description == "" {
			t.Errorf("drill %s has empty description", d.Name)
		}
		if d.Category == "" {
			t.Errorf("drill %s has empty category", d.Name)
		}
		if d.Timeout <= 0 {
			t.Errorf("drill %s has non-positive timeout", d.Name)
		}
	}
}

func TestDrillDefinition_MarshalJSON(t *testing.T) {
	def := DrillDefinition{
		Name:        ScenarioProbeDisconnect,
		Title:       "Probe disconnect",
		Description: "desc",
		Category:    "connectivity",
		Timeout:     10 * time.Second,
	}
	data, err := def.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	s := string(data)
	if !contains(s, `"timeout_ms":10000`) {
		t.Errorf("expected timeout_ms=10000 in JSON, got: %s", s)
	}
}

// ---------------------------------------------------------------------------
// DrillRunner — passing (happy-path) tests
// ---------------------------------------------------------------------------

func TestDrillRunner_Definitions(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{})
	defs := dr.Definitions()
	if len(defs) == 0 {
		t.Fatal("Definitions() returned empty slice")
	}
}

func TestDrillRunner_UnknownScenario(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{})
	ctx := context.Background()
	result := dr.Run(ctx, "does_not_exist")
	if result.Status != DrillStatusFail {
		t.Errorf("expected fail for unknown scenario, got %s", result.Status)
	}
}

func TestDrillRunner_ProbeDisconnect_Pass(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{Probes: &stubProbeRegistry{}})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioProbeDisconnect)
	if result.Status != DrillStatusPass {
		t.Errorf("expected pass, got %s: %s", result.Status, result.ErrorDetails)
	}
	if len(result.Observations) == 0 {
		t.Error("expected at least one observation")
	}
}

func TestDrillRunner_DBWriteFailure_Pass(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{DB: &stubDBWriter{}})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioDBWriteFailure)
	if result.Status != DrillStatusPass {
		t.Errorf("expected pass, got %s: %s", result.Status, result.ErrorDetails)
	}
}

func TestDrillRunner_LLMTimeout_Pass(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{LLM: &stubLLMClient{timeout: true}})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioLLMTimeout)
	if result.Status != DrillStatusPass {
		t.Errorf("expected pass, got %s: %s", result.Status, result.ErrorDetails)
	}
}

func TestDrillRunner_WebSocketFlood_Pass(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{MQ: newStubMessageQueue(10)})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioWebSocketFlood)
	if result.Status != DrillStatusPass {
		t.Errorf("expected pass, got %s: %s", result.Status, result.ErrorDetails)
	}
}

func TestDrillRunner_ConcurrentJobStorm_Pass(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{Jobs: newStubJobQueue(20)})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioConcurrentJobStorm)
	if result.Status != DrillStatusPass {
		t.Errorf("expected pass, got %s: %s", result.Status, result.ErrorDetails)
	}
}

// All drills pass with nil deps (built-in stubs used).
func TestDrillRunner_AllDrills_NilDeps(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{})
	ctx := context.Background()
	for _, def := range dr.Definitions() {
		t.Run(string(def.Name), func(t *testing.T) {
			result := dr.Run(ctx, def.Name)
			if result.Status != DrillStatusPass {
				t.Errorf("expected pass, got %s: %s", result.Status, result.ErrorDetails)
			}
			if result.ID == "" {
				t.Error("result ID should not be empty")
			}
			if result.RanAt.IsZero() {
				t.Error("result RanAt should not be zero")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DrillRunner — failure-detection tests
// ---------------------------------------------------------------------------

func TestDrillRunner_ProbeDisconnect_DetectsNoOffline(t *testing.T) {
	// brokenProbeRegistry.IsOffline always returns false — drill must catch this.
	dr := NewDrillRunner(DrillRunnerDeps{Probes: &brokenProbeRegistry{}})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioProbeDisconnect)
	if result.Status != DrillStatusFail {
		t.Errorf("expected drill to fail when probe isn't actually marked offline, got %s", result.Status)
	}
}

func TestDrillRunner_ProbeDisconnect_DetectsNoRecovery(t *testing.T) {
	// alwaysOfflineProbeRegistry stays offline after MarkOnline — drill must catch this.
	dr := NewDrillRunner(DrillRunnerDeps{Probes: &alwaysOfflineProbeRegistry{}})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioProbeDisconnect)
	if result.Status != DrillStatusFail {
		t.Errorf("expected drill to fail when probe stays offline after MarkOnline, got %s", result.Status)
	}
}

func TestDrillRunner_LLMTimeout_DetectsNoTimeout(t *testing.T) {
	// stubLLMClient with timeout=false responds immediately — drill should fail because it
	// expects the LLM to time out.
	dr := NewDrillRunner(DrillRunnerDeps{LLM: &stubLLMClient{timeout: false}})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioLLMTimeout)
	if result.Status != DrillStatusFail {
		t.Errorf("expected drill to fail when LLM doesn't time out, got %s", result.Status)
	}
}

func TestDrillRunner_WebSocketFlood_DetectsNoBackpressure(t *testing.T) {
	// noBackpressureMQ accepts everything — drill should flag missing backpressure.
	dr := NewDrillRunner(DrillRunnerDeps{MQ: &noBackpressureMQ{}})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioWebSocketFlood)
	if result.Status != DrillStatusFail {
		t.Errorf("expected drill to fail when no backpressure is applied, got %s", result.Status)
	}
}

func TestDrillRunner_ConcurrentJobStorm_GracefulDrainTimeout(t *testing.T) {
	// deadlockJobQueue blocks Drain — drill should complete (not hang) and still pass
	// because jobs were accepted without deadlock, even if drain timed out.
	dr := NewDrillRunner(DrillRunnerDeps{Jobs: &deadlockJobQueue{}})
	ctx := context.Background()

	done := make(chan DrillResult, 1)
	go func() {
		done <- dr.Run(ctx, ScenarioConcurrentJobStorm)
	}()

	select {
	case result := <-done:
		// The drill should complete because Drain gets a short context inside the impl.
		// Jobs were accepted, so the drill should pass even if drain was slow.
		if result.ID == "" {
			t.Error("result ID empty")
		}
		// We accept either pass or fail here — the key property is that the drill
		// *returned* and didn't block the test goroutine.
	case <-time.After(10 * time.Second):
		t.Fatal("drill did not return within 10s — possible deadlock in test harness")
	}
}

// ---------------------------------------------------------------------------
// DrillResult metadata tests
// ---------------------------------------------------------------------------

func TestDrillResult_HasDuration(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioProbeDisconnect)
	if result.DurationMS < 0 {
		t.Errorf("DurationMS should not be negative, got %d", result.DurationMS)
	}
}

func TestDrillResult_HasRecoveryTime(t *testing.T) {
	dr := NewDrillRunner(DrillRunnerDeps{})
	ctx := context.Background()
	result := dr.Run(ctx, ScenarioProbeDisconnect)
	if result.RecoveryMS < 0 {
		t.Errorf("RecoveryMS should not be negative, got %d", result.RecoveryMS)
	}
}

// ---------------------------------------------------------------------------
// RecoveryVerifier tests
// ---------------------------------------------------------------------------

func TestRecoveryVerifier_ProbeOnline(t *testing.T) {
	reg := &stubProbeRegistry{}
	rv := NewRecoveryVerifier(DrillRunnerDeps{Probes: reg})

	// Not offline → online
	state := rv.Verify(context.Background(), "probe-1")
	if !state.ProbeOnline {
		t.Error("expected probe to be online initially")
	}

	// Mark offline → not online
	_ = reg.MarkOffline("probe-1")
	state = rv.Verify(context.Background(), "probe-1")
	if state.ProbeOnline {
		t.Error("expected probe to be offline after MarkOffline")
	}

	// Mark online again → online
	_ = reg.MarkOnline("probe-1")
	state = rv.Verify(context.Background(), "probe-1")
	if !state.ProbeOnline {
		t.Error("expected probe to be online after MarkOnline")
	}
}

func TestRecoveryVerifier_DBWriteable(t *testing.T) {
	rv := NewRecoveryVerifier(DrillRunnerDeps{DB: &stubDBWriter{}})
	state := rv.Verify(context.Background(), "p")
	if !state.DBWriteable {
		t.Error("expected DB to be writeable with stub")
	}
}

func TestRecoveryVerifier_LLMResponsive(t *testing.T) {
	rv := NewRecoveryVerifier(DrillRunnerDeps{LLM: &stubLLMClient{timeout: false}})
	state := rv.Verify(context.Background(), "p")
	if !state.LLMResponsive {
		t.Error("expected LLM to be responsive with non-timeout stub")
	}
}

func TestRecoveryVerifier_LLMUnresponsive(t *testing.T) {
	rv := NewRecoveryVerifier(DrillRunnerDeps{LLM: &stubLLMClient{timeout: true}})
	state := rv.Verify(context.Background(), "p")
	if state.LLMResponsive {
		t.Error("expected LLM to be unresponsive with timeout stub")
	}
}

func TestRecoveryVerifier_NilDeps(t *testing.T) {
	rv := NewRecoveryVerifier(DrillRunnerDeps{})
	// Should not panic
	state := rv.Verify(context.Background(), "p")
	if !state.ProbeOnline {
		t.Error("nil probe registry stub should report probe as online (never marked offline)")
	}
}

// ---------------------------------------------------------------------------
// DrillStore tests
// ---------------------------------------------------------------------------

func TestDrillStore_SaveAndList(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewDrillStore(dbPath)
	if err != nil {
		t.Fatalf("NewDrillStore: %v", err)
	}
	defer store.Close()

	r := DrillResult{
		ID:           "test-id-1",
		Scenario:     ScenarioProbeDisconnect,
		Status:       DrillStatusPass,
		RanAt:        time.Now().UTC().Truncate(time.Second),
		DurationMS:   42,
		RecoveryMS:   7,
		ErrorDetails: "",
		Observations: []string{"obs1", "obs2"},
	}
	if err := store.Save(r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	results, err := store.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	got := results[0]
	if got.ID != r.ID {
		t.Errorf("ID: want %s, got %s", r.ID, got.ID)
	}
	if got.Scenario != r.Scenario {
		t.Errorf("Scenario: want %s, got %s", r.Scenario, got.Scenario)
	}
	if got.Status != r.Status {
		t.Errorf("Status: want %s, got %s", r.Status, got.Status)
	}
	if got.DurationMS != r.DurationMS {
		t.Errorf("DurationMS: want %d, got %d", r.DurationMS, got.DurationMS)
	}
	if len(got.Observations) != 2 {
		t.Errorf("Observations: want 2, got %d", len(got.Observations))
	}
}

func TestDrillStore_ListLimit(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewDrillStore(dbPath)
	if err != nil {
		t.Fatalf("NewDrillStore: %v", err)
	}
	defer store.Close()

	for i := 0; i < 5; i++ {
		_ = store.Save(DrillResult{
			ID:           fmt.Sprintf("id-%d", i),
			Scenario:     ScenarioDBWriteFailure,
			Status:       DrillStatusPass,
			RanAt:        time.Now().UTC(),
			Observations: []string{},
		})
	}

	results, err := store.List(3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit=3, got %d", len(results))
	}
}

func TestDrillStore_EmptyList(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewDrillStore(dbPath)
	if err != nil {
		t.Fatalf("NewDrillStore: %v", err)
	}
	defer store.Close()

	results, err := store.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty list, got %d", len(results))
	}
}

func TestDrillStore_Reopen(t *testing.T) {
	dbPath := tempDBPath(t)

	store, err := NewDrillStore(dbPath)
	if err != nil {
		t.Fatalf("NewDrillStore: %v", err)
	}
	_ = store.Save(DrillResult{
		ID:           "persist-1",
		Scenario:     ScenarioLLMTimeout,
		Status:       DrillStatusFail,
		RanAt:        time.Now().UTC(),
		ErrorDetails: "some error",
		Observations: []string{},
	})
	store.Close()

	store2, err := NewDrillStore(dbPath)
	if err != nil {
		t.Fatalf("NewDrillStore reopen: %v", err)
	}
	defer store2.Close()

	results, err := store2.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 persisted result, got %d", len(results))
	}
	if results[0].ErrorDetails != "some error" {
		t.Errorf("ErrorDetails not persisted: %s", results[0].ErrorDetails)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func tempDBPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "drills-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	t.Cleanup(func() { os.Remove(name) })
	return name
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
