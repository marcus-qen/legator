package modeldock

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newTestTrialDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "trial_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestTrialStore(t *testing.T) *TrialStore {
	t.Helper()
	db := newTestTrialDB(t)
	ts, err := NewTrialStore(db)
	if err != nil {
		t.Fatalf("new trial store: %v", err)
	}
	return ts
}

func sampleTrial() Trial {
	return Trial{
		Name:        "Latency Benchmark",
		Description: "Compares two models on latency",
		Prompts: []TrialPrompt{
			{ID: "p1", User: "What is 2+2?", Criteria: EvalCriteria{Type: EvalContains, Expected: "4"}},
			{ID: "p2", User: "Name the capital of France.", Criteria: EvalCriteria{Type: EvalContains, Expected: "Paris"}},
		},
		Models: []TrialModel{
			{ProfileID: "model-a", Label: "Model A"},
			{ProfileID: "model-b", Label: "Model B"},
		},
		Parameters: TrialParameters{Temperature: 0.7, MaxTokens: 256, TimeoutSecs: 30},
	}
}

// ── Trial CRUD ──────────────────────────────────────────────

func TestTrialStore_CreateAndGet(t *testing.T) {
	ts := newTestTrialStore(t)
	trial, err := ts.CreateTrial(sampleTrial())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if trial.ID == "" {
		t.Error("expected non-empty ID")
	}
	if trial.Name != "Latency Benchmark" {
		t.Errorf("name: got %q", trial.Name)
	}
	if len(trial.Prompts) != 2 {
		t.Errorf("prompts: want 2, got %d", len(trial.Prompts))
	}
	if len(trial.Models) != 2 {
		t.Errorf("models: want 2, got %d", len(trial.Models))
	}

	got, err := ts.GetTrial(trial.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != trial.Name {
		t.Errorf("round-trip name: %q", got.Name)
	}
}

func TestTrialStore_GetNotFound(t *testing.T) {
	ts := newTestTrialStore(t)
	_, err := ts.GetTrial("does-not-exist")
	if err == nil {
		t.Error("expected error for missing trial")
	}
	if !IsNotFound(err) {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

func TestTrialStore_ListTrials(t *testing.T) {
	ts := newTestTrialStore(t)
	for i := 0; i < 3; i++ {
		tr := sampleTrial()
		tr.Name = "Trial " + string(rune('A'+i))
		if _, err := ts.CreateTrial(tr); err != nil {
			t.Fatalf("create trial %d: %v", i, err)
		}
	}
	trials, err := ts.ListTrials()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(trials) != 3 {
		t.Errorf("want 3 trials, got %d", len(trials))
	}
}

// ── TrialRun CRUD ───────────────────────────────────────────

func TestTrialStore_RunLifecycle(t *testing.T) {
	ts := newTestTrialStore(t)
	trial, _ := ts.CreateTrial(sampleTrial())

	run, err := ts.CreateRun(trial.ID)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.Status != TrialRunPending {
		t.Errorf("initial status: want pending, got %s", run.Status)
	}

	if err := ts.UpdateRunStatus(run.ID, TrialRunRunning, ""); err != nil {
		t.Fatalf("update running: %v", err)
	}
	got, _ := ts.GetRun(run.ID)
	if got.Status != TrialRunRunning {
		t.Errorf("want running, got %s", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("started_at should be set")
	}

	if err := ts.UpdateRunStatus(run.ID, TrialRunCompleted, ""); err != nil {
		t.Fatalf("update completed: %v", err)
	}
	got, _ = ts.GetRun(run.ID)
	if got.Status != TrialRunCompleted {
		t.Errorf("want completed, got %s", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at should be set")
	}
}

func TestTrialStore_ListRuns(t *testing.T) {
	ts := newTestTrialStore(t)
	trial, _ := ts.CreateTrial(sampleTrial())

	for i := 0; i < 3; i++ {
		if _, err := ts.CreateRun(trial.ID); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}

	runs, err := ts.ListRuns(trial.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 3 {
		t.Errorf("want 3 runs, got %d", len(runs))
	}
}

// ── TrialResult CRUD + Aggregation ──────────────────────────

func TestTrialStore_SaveAndListResults(t *testing.T) {
	ts := newTestTrialStore(t)
	trial, _ := ts.CreateTrial(sampleTrial())
	run, _ := ts.CreateRun(trial.ID)

	results := []TrialResult{
		{RunID: run.ID, ProfileID: "model-a", PromptID: "p1", Response: "4", TotalLatencyMs: 100, TTFT: 100, PromptTokens: 10, CompletionTokens: 5, QualityScore: 1.0, CreatedAt: time.Now().UTC()},
		{RunID: run.ID, ProfileID: "model-a", PromptID: "p2", Response: "Paris", TotalLatencyMs: 120, TTFT: 120, PromptTokens: 12, CompletionTokens: 4, QualityScore: 1.0, CreatedAt: time.Now().UTC()},
		{RunID: run.ID, ProfileID: "model-b", PromptID: "p1", Response: "Four", TotalLatencyMs: 200, TTFT: 200, PromptTokens: 10, CompletionTokens: 3, QualityScore: 0.0, CreatedAt: time.Now().UTC()},
		{RunID: run.ID, ProfileID: "model-b", PromptID: "p2", Response: "Paris, France", TotalLatencyMs: 220, TTFT: 220, PromptTokens: 12, CompletionTokens: 6, QualityScore: 1.0, CreatedAt: time.Now().UTC()},
	}

	for _, r := range results {
		if _, err := ts.SaveResult(r); err != nil {
			t.Fatalf("save result: %v", err)
		}
	}

	listed, err := ts.ListResults(run.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 4 {
		t.Errorf("want 4 results, got %d", len(listed))
	}
}

func TestTrialStore_AggregateByModel(t *testing.T) {
	ts := newTestTrialStore(t)
	trial, _ := ts.CreateTrial(sampleTrial())
	run, _ := ts.CreateRun(trial.ID)

	for _, r := range []TrialResult{
		{RunID: run.ID, ProfileID: "fast", PromptID: "p1", TotalLatencyMs: 50, TTFT: 50, PromptTokens: 10, CompletionTokens: 5, CostEstimateUSD: 0.001, QualityScore: 1.0, CreatedAt: time.Now().UTC()},
		{RunID: run.ID, ProfileID: "fast", PromptID: "p2", TotalLatencyMs: 70, TTFT: 70, PromptTokens: 12, CompletionTokens: 4, CostEstimateUSD: 0.001, QualityScore: 1.0, CreatedAt: time.Now().UTC()},
		{RunID: run.ID, ProfileID: "slow", PromptID: "p1", TotalLatencyMs: 300, TTFT: 300, PromptTokens: 10, CompletionTokens: 8, CostEstimateUSD: 0.005, QualityScore: 0.5, CreatedAt: time.Now().UTC()},
		{RunID: run.ID, ProfileID: "slow", PromptID: "p2", TotalLatencyMs: 400, TTFT: 400, PromptTokens: 12, CompletionTokens: 7, CostEstimateUSD: 0.005, QualityScore: 0.5, CreatedAt: time.Now().UTC()},
	} {
		_, _ = ts.SaveResult(r)
	}

	aggs, err := ts.AggregateByModel(run.ID)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	fast, ok := aggs["fast"]
	if !ok {
		t.Fatal("expected 'fast' model in aggs")
	}
	if fast.AvgLatencyMs >= aggs["slow"].AvgLatencyMs {
		t.Errorf("fast avg latency (%v) should be < slow (%v)", fast.AvgLatencyMs, aggs["slow"].AvgLatencyMs)
	}
	if fast.AvgQualityScore != 1.0 {
		t.Errorf("fast quality: want 1.0, got %v", fast.AvgQualityScore)
	}
	if fast.PromptCount != 2 {
		t.Errorf("fast prompt count: want 2, got %d", fast.PromptCount)
	}
	if aggs["slow"].TotalCostUSD <= fast.TotalCostUSD {
		t.Error("slow should cost more than fast")
	}
}

func TestTrialStore_P95Latency(t *testing.T) {
	ts := newTestTrialStore(t)
	trial, _ := ts.CreateTrial(sampleTrial())
	run, _ := ts.CreateRun(trial.ID)

	// Insert 10 results with latencies 100..1000 ms.
	for i := 1; i <= 10; i++ {
		_, _ = ts.SaveResult(TrialResult{
			RunID: run.ID, ProfileID: "m",
			PromptID:       "p" + string(rune('0'+i)),
			TotalLatencyMs: int64(i * 100), TTFT: int64(i * 100),
			CreatedAt: time.Now().UTC(),
		})
	}

	aggs, err := ts.AggregateByModel(run.ID)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	m := aggs["m"]
	if m.P95LatencyMs < 900 {
		t.Errorf("p95 should be near 1000, got %v", m.P95LatencyMs)
	}
	if m.P50LatencyMs < 450 || m.P50LatencyMs > 600 {
		t.Errorf("p50 should be near 500, got %v", m.P50LatencyMs)
	}
}
