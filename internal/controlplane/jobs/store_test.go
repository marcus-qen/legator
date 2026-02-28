package jobs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createTestJob(t *testing.T, store *Store) *Job {
	t.Helper()
	job, err := store.CreateJob(Job{
		Name:     "disk check",
		Command:  "echo ok",
		Schedule: "5m",
		Target: Target{
			Kind:  TargetKindProbe,
			Value: "probe-1",
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

func TestStoreCreateGetUpdateDeleteListJobs(t *testing.T) {
	store := newTestStore(t)

	created := createTestJob(t, store)
	if created.ID == "" {
		t.Fatal("expected generated id")
	}

	fetched, err := store.GetJob(created.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if fetched.Name != created.Name {
		t.Fatalf("expected name %q, got %q", created.Name, fetched.Name)
	}

	updated, err := store.UpdateJob(Job{
		ID:         created.ID,
		Name:       "disk check v2",
		Command:    "echo updated",
		Schedule:   "*/5 * * * *",
		Target:     Target{Kind: TargetKindTag, Value: "prod"},
		Enabled:    true,
		CreatedAt:  fetched.CreatedAt,
		LastRunAt:  fetched.LastRunAt,
		LastStatus: fetched.LastStatus,
	})
	if err != nil {
		t.Fatalf("update job: %v", err)
	}
	if updated.Name != "disk check v2" {
		t.Fatalf("unexpected updated name: %q", updated.Name)
	}
	if updated.Target.Kind != TargetKindTag || updated.Target.Value != "prod" {
		t.Fatalf("unexpected target after update: %#v", updated.Target)
	}

	disabled, err := store.SetEnabled(created.ID, false)
	if err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("expected job disabled")
	}

	list, err := store.ListJobs()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 job, got %d", len(list))
	}

	if err := store.DeleteJob(created.ID); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	if _, err := store.GetJob(created.ID); !IsNotFound(err) {
		t.Fatalf("expected not found after delete, got err=%v", err)
	}
}

func TestStoreRunHistoryAndAutoPrune(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	job, err := store.CreateJob(Job{
		Name:     "uptime",
		Command:  "uptime",
		Schedule: "1m",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	oldStart := time.Now().UTC().Add(-8 * 24 * time.Hour)
	oldRun, err := store.RecordRunStart(JobRun{
		JobID:     job.ID,
		ProbeID:   "probe-1",
		RequestID: "job-old-run",
		StartedAt: oldStart,
	})
	if err != nil {
		t.Fatalf("record old run: %v", err)
	}
	if err := store.CompleteRun(oldRun.ID, RunStatusSuccess, intPtr(0), "old"); err != nil {
		t.Fatalf("complete old run: %v", err)
	}

	recentRun, err := store.RecordRunStart(JobRun{
		JobID:     job.ID,
		ProbeID:   "probe-1",
		RequestID: "job-recent-run",
	})
	if err != nil {
		t.Fatalf("record recent run: %v", err)
	}
	bigOutput := strings.Repeat("x", maxRunOutputBytes+512)
	if err := store.CompleteRun(recentRun.ID, RunStatusFailed, intPtr(2), bigOutput); err != nil {
		t.Fatalf("complete recent run: %v", err)
	}

	_ = store.Close()

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	runs, err := reopened.ListRunsByJob(job.ID, 50)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run after prune, got %d", len(runs))
	}
	if runs[0].RequestID != "job-recent-run" {
		t.Fatalf("unexpected run after prune: %s", runs[0].RequestID)
	}
	if len(runs[0].Output) > maxRunOutputBytes {
		t.Fatalf("expected output truncation <= %d bytes, got %d", maxRunOutputBytes, len(runs[0].Output))
	}
}

func intPtr(v int) *int { return &v }
