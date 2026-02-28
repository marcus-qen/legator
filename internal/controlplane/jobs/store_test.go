package jobs

import (
	"path/filepath"
	"strings"
	"sync"
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

func TestStoreListRunsFilters(t *testing.T) {
	store := newTestStore(t)
	job := createTestJob(t, store)

	base := time.Now().UTC().Add(-time.Minute)
	runSuccess, err := store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-1", RequestID: "list-success", StartedAt: base})
	if err != nil {
		t.Fatalf("record success run: %v", err)
	}
	if err := store.CompleteRun(runSuccess.ID, RunStatusSuccess, intPtr(0), "ok"); err != nil {
		t.Fatalf("complete success run: %v", err)
	}

	runFailed, err := store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-2", RequestID: "list-failed", StartedAt: base.Add(10 * time.Second)})
	if err != nil {
		t.Fatalf("record failed run: %v", err)
	}
	if err := store.CompleteRun(runFailed.ID, RunStatusFailed, intPtr(2), "failed"); err != nil {
		t.Fatalf("complete failed run: %v", err)
	}

	runs, err := store.ListRuns(RunQuery{JobID: job.ID, Status: RunStatusFailed, Limit: 10})
	if err != nil {
		t.Fatalf("list filtered runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one failed run, got %d", len(runs))
	}
	if runs[0].RequestID != "list-failed" {
		t.Fatalf("unexpected run returned: %s", runs[0].RequestID)
	}

	after := base.Add(5 * time.Second)
	runs, err = store.ListRuns(RunQuery{JobID: job.ID, StartedAfter: &after, Limit: 10})
	if err != nil {
		t.Fatalf("list runs by time: %v", err)
	}
	if len(runs) != 1 || runs[0].RequestID != "list-failed" {
		t.Fatalf("expected only failed run in time filter, got %#v", runs)
	}
}

func TestStoreCompleteRunStatusTransitionsWithFanout(t *testing.T) {
	store := newTestStore(t)
	job := createTestJob(t, store)

	started := time.Now().UTC().Add(-time.Minute)
	runA, err := store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-a", RequestID: "fanout-a", StartedAt: started})
	if err != nil {
		t.Fatalf("record runA: %v", err)
	}
	runB, err := store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-b", RequestID: "fanout-b", StartedAt: started})
	if err != nil {
		t.Fatalf("record runB: %v", err)
	}

	if err := store.CompleteRun(runA.ID, RunStatusSuccess, intPtr(0), "ok"); err != nil {
		t.Fatalf("complete runA: %v", err)
	}
	mid, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job mid: %v", err)
	}
	if mid.LastStatus != RunStatusRunning {
		t.Fatalf("expected status running while fanout still active, got %s", mid.LastStatus)
	}

	if err := store.CompleteRun(runB.ID, RunStatusFailed, intPtr(1), "boom"); err != nil {
		t.Fatalf("complete runB: %v", err)
	}
	ended, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job ended: %v", err)
	}
	if ended.LastStatus != RunStatusFailed {
		t.Fatalf("expected status failed after fanout completion, got %s", ended.LastStatus)
	}

	newerStart := started.Add(2 * time.Minute)
	runC, err := store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-c", RequestID: "fanout-c", StartedAt: newerStart})
	if err != nil {
		t.Fatalf("record runC: %v", err)
	}
	afterStart, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job after start: %v", err)
	}
	if afterStart.LastStatus != RunStatusRunning {
		t.Fatalf("expected running after newer batch start, got %s", afterStart.LastStatus)
	}
	if err := store.CompleteRun(runC.ID, RunStatusSuccess, intPtr(0), "ok"); err != nil {
		t.Fatalf("complete runC: %v", err)
	}
	final, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job final: %v", err)
	}
	if final.LastStatus != RunStatusSuccess {
		t.Fatalf("expected final success, got %s", final.LastStatus)
	}
}

func TestStorePendingRunningAndCompletionStateMachine(t *testing.T) {
	store := newTestStore(t)
	job := createTestJob(t, store)

	run, err := store.RecordRunStart(JobRun{
		JobID:     job.ID,
		ProbeID:   "probe-1",
		RequestID: "state-machine",
		Status:    RunStatusPending,
	})
	if err != nil {
		t.Fatalf("record pending run: %v", err)
	}

	if err := store.CompleteRun(run.ID, RunStatusSuccess, intPtr(0), "ok"); err == nil {
		t.Fatal("expected pending->success to fail")
	}

	if err := store.MarkRunRunning(run.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if err := store.CompleteRun(run.ID, RunStatusSuccess, intPtr(0), "ok"); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	updated, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status != RunStatusSuccess {
		t.Fatalf("expected success, got %s", updated.Status)
	}

	ended, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if ended.LastStatus != RunStatusSuccess {
		t.Fatalf("expected job success, got %s", ended.LastStatus)
	}
}

func TestStoreCancelTransitionAndTerminalImmutability(t *testing.T) {
	store := newTestStore(t)
	job := createTestJob(t, store)

	run, err := store.RecordRunStart(JobRun{
		JobID:     job.ID,
		ProbeID:   "probe-1",
		RequestID: "cancel-transition",
		Status:    RunStatusPending,
	})
	if err != nil {
		t.Fatalf("record pending run: %v", err)
	}

	if err := store.CancelRun(run.ID, "canceled for test"); err != nil {
		t.Fatalf("cancel run: %v", err)
	}

	updated, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status != RunStatusCanceled {
		t.Fatalf("expected canceled, got %s", updated.Status)
	}

	if err := store.MarkRunRunning(run.ID); !IsInvalidRunTransition(err) {
		t.Fatalf("expected invalid transition on canceled->running, got %v", err)
	}
	if err := store.CompleteRun(run.ID, RunStatusFailed, intPtr(1), "late fail"); !IsInvalidRunTransition(err) {
		t.Fatalf("expected invalid transition on canceled->failed, got %v", err)
	}
}

func TestStoreRaceCompleteVsCancelOnlyOneWins(t *testing.T) {
	store := newTestStore(t)
	job := createTestJob(t, store)

	run, err := store.RecordRunStart(JobRun{
		JobID:     job.ID,
		ProbeID:   "probe-1",
		RequestID: "race-complete-cancel",
		Status:    RunStatusRunning,
	})
	if err != nil {
		t.Fatalf("record run: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)

	go func() {
		defer wg.Done()
		results <- store.CompleteRun(run.ID, RunStatusSuccess, intPtr(0), "ok")
	}()
	go func() {
		defer wg.Done()
		results <- store.CancelRun(run.ID, "cancel race")
	}()

	wg.Wait()
	close(results)

	successes := 0
	invalids := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case IsInvalidRunTransition(err):
			invalids++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || invalids != 1 {
		t.Fatalf("expected one winner and one invalid transition, got successes=%d invalid=%d", successes, invalids)
	}

	updated, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status != RunStatusSuccess && updated.Status != RunStatusCanceled {
		t.Fatalf("unexpected terminal status: %s", updated.Status)
	}
}

func TestStorePersistsRetryPolicyAndAttemptMetadata(t *testing.T) {
	store := newTestStore(t)
	job, err := store.CreateJob(Job{
		Name:     "retry-meta",
		Command:  "false",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		RetryPolicy: &RetryPolicy{
			MaxAttempts:    5,
			InitialBackoff: "3s",
			Multiplier:     2.5,
			MaxBackoff:     "20s",
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.RetryPolicy == nil || job.RetryPolicy.MaxAttempts != 5 {
		t.Fatalf("expected retry policy to persist, got %#v", job.RetryPolicy)
	}

	run, err := store.RecordRunStart(JobRun{
		JobID:            job.ID,
		ProbeID:          "probe-1",
		RequestID:        "retry-meta-attempt-2",
		ExecutionID:      "exec-1",
		Attempt:          2,
		MaxAttempts:      5,
		Status:           RunStatusRunning,
		RetryScheduledAt: nil,
	})
	if err != nil {
		t.Fatalf("record run: %v", err)
	}
	retryAt := time.Now().UTC().Add(5 * time.Second)
	if err := store.CompleteRunWithRetry(run.ID, RunStatusFailed, intPtr(1), "boom", &retryAt); err != nil {
		t.Fatalf("complete run with retry: %v", err)
	}

	storedRun, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if storedRun.Attempt != 2 || storedRun.MaxAttempts != 5 {
		t.Fatalf("unexpected attempt metadata: attempt=%d max=%d", storedRun.Attempt, storedRun.MaxAttempts)
	}
	if storedRun.ExecutionID != "exec-1" {
		t.Fatalf("unexpected execution id: %s", storedRun.ExecutionID)
	}
	if storedRun.RetryScheduledAt == nil {
		t.Fatal("expected retry_scheduled_at to be stored")
	}
}

func intPtr(v int) *int { return &v }
