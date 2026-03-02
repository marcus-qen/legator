package jobs

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAsyncJobStateMachineTransitions(t *testing.T) {
	store := newTestStore(t)

	created, err := store.CreateAsyncJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-1", Command: "uname -a"})
	if err != nil {
		t.Fatalf("create async job: %v", err)
	}
	if created.State != AsyncJobStateQueued {
		t.Fatalf("expected queued, got %s", created.State)
	}

	running, err := store.TransitionAsyncJob(created.ID, AsyncJobStateRunning, AsyncJobTransitionOptions{})
	if err != nil {
		t.Fatalf("transition to running: %v", err)
	}
	if running.StartedAt == nil {
		t.Fatalf("expected started_at set")
	}

	succeeded, err := store.TransitionAsyncJob(created.ID, AsyncJobStateSucceeded, AsyncJobTransitionOptions{Output: "ok", ExitCode: intPtr(0)})
	if err != nil {
		t.Fatalf("transition to succeeded: %v", err)
	}
	if succeeded.FinishedAt == nil {
		t.Fatalf("expected finished_at set")
	}

	_, err = store.TransitionAsyncJob(created.ID, AsyncJobStateRunning, AsyncJobTransitionOptions{})
	if !errors.Is(err, ErrInvalidAsyncJobTransition) {
		t.Fatalf("expected invalid transition error, got %v", err)
	}
}

func TestAsyncJobWaitingApprovalCancel(t *testing.T) {
	store := newTestStore(t)
	job, err := store.CreateAsyncJob(AsyncJob{ProbeID: "probe-2", RequestID: "req-2", Command: "systemctl restart nginx"})
	if err != nil {
		t.Fatalf("create async job: %v", err)
	}

	expires := time.Now().UTC().Add(5 * time.Minute)
	waiting, err := store.TransitionAsyncJob(job.ID, AsyncJobStateWaitingApproval, AsyncJobTransitionOptions{
		ApprovalID:   "apr-1",
		StatusReason: "waiting for human approval",
		ExpiresAt:    &expires,
	})
	if err != nil {
		t.Fatalf("transition waiting approval: %v", err)
	}
	if waiting.ApprovalID != "apr-1" {
		t.Fatalf("expected approval id apr-1, got %s", waiting.ApprovalID)
	}

	cancelled, err := store.CancelAsyncJob(job.ID, "cancelled by operator")
	if err != nil {
		t.Fatalf("cancel async job: %v", err)
	}
	if cancelled.State != AsyncJobStateCancelled {
		t.Fatalf("expected cancelled, got %s", cancelled.State)
	}
}

func TestAsyncJobMigrationAndManagerRecovery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	first, err := store.CreateAsyncJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-running", Command: "long command"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := store.TransitionAsyncJob(first.ID, AsyncJobStateRunning, AsyncJobTransitionOptions{}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	expiredApprovalAt := time.Now().UTC().Add(-time.Minute)
	second, err := store.CreateAsyncJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-waiting", Command: "needs approval"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if _, err := store.TransitionAsyncJob(second.ID, AsyncJobStateWaitingApproval, AsyncJobTransitionOptions{ApprovalID: "apr-x", ExpiresAt: &expiredApprovalAt}); err != nil {
		t.Fatalf("mark waiting: %v", err)
	}
	_ = store.Close()

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	manager := NewAsyncManager(reopened)
	runningExpired, waitingExpired, err := manager.ExpireStale(time.Now().UTC())
	if err != nil {
		t.Fatalf("expire stale: %v", err)
	}
	if runningExpired != 1 {
		t.Fatalf("expected 1 running expired, got %d", runningExpired)
	}
	if waitingExpired != 1 {
		t.Fatalf("expected 1 waiting approval expired, got %d", waitingExpired)
	}

	jobs, err := reopened.ListAsyncJobs(10)
	if err != nil {
		t.Fatalf("list async jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
}

func TestAsyncStoreListExpiredWaitingApprovalJobsAndExtend(t *testing.T) {
	store := newTestStore(t)
	job, err := store.CreateAsyncJob(AsyncJob{ProbeID: "probe-exp", RequestID: "req-exp", Command: "systemctl restart nginx"})
	if err != nil {
		t.Fatalf("create async job: %v", err)
	}
	expiresAt := time.Now().UTC().Add(-2 * time.Second)
	if _, err := store.TransitionAsyncJob(job.ID, AsyncJobStateWaitingApproval, AsyncJobTransitionOptions{
		ApprovalID: "apr-exp",
		ExpiresAt:  &expiresAt,
	}); err != nil {
		t.Fatalf("mark waiting approval: %v", err)
	}

	expired, err := store.ListExpiredWaitingApprovalJobs(time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("list expired waiting jobs: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired waiting job, got %d", len(expired))
	}
	if expired[0].ID != job.ID {
		t.Fatalf("unexpected expired job id %s", expired[0].ID)
	}

	newExpiry := time.Now().UTC().Add(10 * time.Minute)
	if err := store.ExtendApprovalExpiry(job.ID, newExpiry); err != nil {
		t.Fatalf("extend approval expiry: %v", err)
	}

	expired, err = store.ListExpiredWaitingApprovalJobs(time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("list expired waiting jobs after extend: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("expected 0 expired waiting jobs after extend, got %d", len(expired))
	}
}

func TestAsyncManagerApproveAndRejectWaitingJob(t *testing.T) {
	store := newTestStore(t)
	manager := NewAsyncManager(store)

	approveSeed, err := manager.CreateJob(AsyncJob{ProbeID: "probe-a", RequestID: "req-a", Command: "echo ok"})
	if err != nil {
		t.Fatalf("create approve seed job: %v", err)
	}
	if _, err := manager.MarkWaitingApproval(approveSeed.ID, "apr-a", "waiting approval", nil); err != nil {
		t.Fatalf("mark waiting approval: %v", err)
	}
	running, err := manager.ApproveJob(approveSeed.ID)
	if err != nil {
		t.Fatalf("approve waiting job: %v", err)
	}
	if running.State != AsyncJobStateRunning {
		t.Fatalf("expected running after approve, got %s", running.State)
	}

	rejectSeed, err := manager.CreateJob(AsyncJob{ProbeID: "probe-r", RequestID: "req-r", Command: "echo no"})
	if err != nil {
		t.Fatalf("create reject seed job: %v", err)
	}
	if _, err := manager.MarkWaitingApproval(rejectSeed.ID, "apr-r", "waiting approval", nil); err != nil {
		t.Fatalf("mark waiting approval reject seed: %v", err)
	}
	failed, err := manager.RejectJob(rejectSeed.ID, "operator rejected")
	if err != nil {
		t.Fatalf("reject waiting job: %v", err)
	}
	if failed.State != AsyncJobStateFailed {
		t.Fatalf("expected failed after reject, got %s", failed.State)
	}
	if failed.StatusReason != "operator rejected" {
		t.Fatalf("unexpected reject reason %q", failed.StatusReason)
	}

	_, err = manager.RejectJob(rejectSeed.ID, "again")
	if !IsAsyncJobConflict(err) {
		t.Fatalf("expected async job conflict on second reject, got %v", err)
	}
}
