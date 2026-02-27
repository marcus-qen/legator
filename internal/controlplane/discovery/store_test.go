package discovery

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "discovery.db"))
	if err != nil {
		t.Fatalf("new discovery store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreCreateCompleteAndGetRun(t *testing.T) {
	store := newTestStore(t)

	run, err := store.CreateRun("192.168.1.0/24", time.Now().UTC())
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.ID == 0 {
		t.Fatal("expected run id to be set")
	}
	if run.Status != StatusRunning {
		t.Fatalf("expected status running, got %q", run.Status)
	}

	if err := store.CompleteRun(run.ID, StatusCompleted, "", time.Now().UTC()); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	reloaded, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if reloaded.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q", reloaded.Status)
	}
	if reloaded.CompletedAt == nil {
		t.Fatal("expected completed_at to be set")
	}
}

func TestStoreCandidatesAndHistory(t *testing.T) {
	store := newTestStore(t)

	run, err := store.CreateRun("10.0.0.0/24", time.Now().UTC())
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	candidates := []Candidate{
		{IP: "10.0.0.10", Hostname: "web-01", OpenPorts: []int{22, 443}, Confidence: ConfidenceHigh},
		{IP: "10.0.0.11", Hostname: "", OpenPorts: []int{}, Confidence: ConfidenceLow},
	}
	if err := store.ReplaceCandidates(run.ID, candidates); err != nil {
		t.Fatalf("replace candidates: %v", err)
	}
	if err := store.CompleteRun(run.ID, StatusCompleted, "", time.Now().UTC()); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	resp, err := store.GetRunWithCandidates(run.ID)
	if err != nil {
		t.Fatalf("get run with candidates: %v", err)
	}
	if len(resp.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(resp.Candidates))
	}

	runs, err := store.ListRuns(10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("expected at least one run in history")
	}
}
