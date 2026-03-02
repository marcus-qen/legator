package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestAsyncManagerCreateJobRejectsWhenQueueSaturated(t *testing.T) {
	store := newTestStore(t)
	manager := NewAsyncManager(store, WithAsyncMaxQueueDepth(1))

	if _, err := manager.CreateJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-1", Command: "echo one"}); err != nil {
		t.Fatalf("create first async job: %v", err)
	}
	_, err := manager.CreateJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-2", Command: "echo two"})
	if err == nil {
		t.Fatal("expected queue saturation error")
	}
	if !IsAsyncQueueSaturated(err) {
		t.Fatalf("expected queue saturated error, got %v", err)
	}

	var saturation *AsyncQueueSaturatedError
	if !errors.As(err, &saturation) {
		t.Fatalf("expected AsyncQueueSaturatedError, got %T", err)
	}
	if saturation.Queued != 1 || saturation.MaxDepth != 1 {
		t.Fatalf("unexpected saturation payload: %+v", saturation)
	}
}

func TestAsyncWorkerSchedulerDispatchNowQueuesWhenGlobalLimitReached(t *testing.T) {
	store := newTestStore(t)
	manager := NewAsyncManager(store)

	running, err := manager.CreateJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-running", Command: "sleep"})
	if err != nil {
		t.Fatalf("create running seed job: %v", err)
	}
	if _, err := store.TransitionAsyncJob(running.ID, AsyncJobStateRunning, AsyncJobTransitionOptions{}); err != nil {
		t.Fatalf("seed running state: %v", err)
	}

	queued, err := manager.CreateJob(AsyncJob{ProbeID: "probe-2", RequestID: "req-queued", Command: "echo hi"})
	if err != nil {
		t.Fatalf("create queued job: %v", err)
	}

	dispatches := 0
	scheduler := NewAsyncWorkerScheduler(store, AsyncJobDispatcherFunc(func(ctx context.Context, job AsyncJob) error {
		dispatches++
		return nil
	}), zap.NewNop(), AsyncWorkerConfig{MaxInFlight: 1, PollInterval: 20 * time.Millisecond})

	result, err := scheduler.DispatchNow(queued.ID)
	if err != nil {
		t.Fatalf("dispatch now: %v", err)
	}
	if result.Outcome != AsyncDispatchOutcomeQueued {
		t.Fatalf("expected queued outcome, got %+v", result)
	}
	if result.Reason != "global_limit" {
		t.Fatalf("expected global_limit reason, got %q", result.Reason)
	}
	if dispatches != 0 {
		t.Fatalf("expected zero dispatches while saturated, got %d", dispatches)
	}
}

func TestAsyncWorkerSchedulerDrainsQueueWithPerProbeCap(t *testing.T) {
	store := newTestStore(t)
	manager := NewAsyncManager(store)

	running, err := manager.CreateJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-running", Command: "running"})
	if err != nil {
		t.Fatalf("create running job: %v", err)
	}
	if _, err := store.TransitionAsyncJob(running.ID, AsyncJobStateRunning, AsyncJobTransitionOptions{}); err != nil {
		t.Fatalf("transition running seed: %v", err)
	}

	probeOneQueued, err := manager.CreateJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-probe1", Command: "echo p1"})
	if err != nil {
		t.Fatalf("create probe-1 queued: %v", err)
	}
	probeTwoQueued, err := manager.CreateJob(AsyncJob{ProbeID: "probe-2", RequestID: "req-probe2", Command: "echo p2"})
	if err != nil {
		t.Fatalf("create probe-2 queued: %v", err)
	}

	dispatched := make(chan string, 2)
	scheduler := NewAsyncWorkerScheduler(store, AsyncJobDispatcherFunc(func(ctx context.Context, job AsyncJob) error {
		dispatched <- job.ID
		return nil
	}), zap.NewNop(), AsyncWorkerConfig{MaxInFlight: 2, MaxPerProbe: 1, PollInterval: 10 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.Start(ctx)
	defer func() {
		cancel()
		scheduler.Stop()
	}()

	select {
	case got := <-dispatched:
		if got != probeTwoQueued.ID {
			t.Fatalf("expected probe-2 queued job to dispatch first, got %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for queued dispatch")
	}

	select {
	case got := <-dispatched:
		t.Fatalf("unexpected additional dispatch while probe cap reached: %s", got)
	case <-time.After(150 * time.Millisecond):
		// expected: probe-1 queued remains deferred by per-probe cap
	}

	jobOne, err := store.GetAsyncJob(probeOneQueued.ID)
	if err != nil {
		t.Fatalf("get probe-1 queued job: %v", err)
	}
	if jobOne.State != AsyncJobStateQueued {
		t.Fatalf("expected probe-1 queued job to remain queued, got %s", jobOne.State)
	}

	jobTwo, err := store.GetAsyncJob(probeTwoQueued.ID)
	if err != nil {
		t.Fatalf("get probe-2 job: %v", err)
	}
	if jobTwo.State != AsyncJobStateRunning {
		t.Fatalf("expected probe-2 job running after dispatch, got %s", jobTwo.State)
	}
}

func TestAsyncWorkerSchedulerDispatchFailureMarksJobFailed(t *testing.T) {
	store := newTestStore(t)
	manager := NewAsyncManager(store)
	queued, err := manager.CreateJob(AsyncJob{ProbeID: "probe-1", RequestID: "req-fail", Command: "echo fail"})
	if err != nil {
		t.Fatalf("create queued job: %v", err)
	}

	scheduler := NewAsyncWorkerScheduler(store, AsyncJobDispatcherFunc(func(ctx context.Context, job AsyncJob) error {
		return errors.New("probe not connected")
	}), zap.NewNop(), AsyncWorkerConfig{MaxInFlight: 1})

	if _, err := scheduler.DispatchNow(queued.ID); err == nil {
		t.Fatal("expected dispatch error")
	}

	updated, err := store.GetAsyncJob(queued.ID)
	if err != nil {
		t.Fatalf("get failed job: %v", err)
	}
	if updated.State != AsyncJobStateFailed {
		t.Fatalf("expected failed state, got %s", updated.State)
	}
	if updated.StatusReason == "" {
		t.Fatalf("expected status reason on failed state")
	}
}
