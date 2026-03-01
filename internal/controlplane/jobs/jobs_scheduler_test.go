package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func TestIsScheduleDueInterval(t *testing.T) {
	now := time.Date(2026, 2, 28, 8, 30, 0, 0, time.UTC)
	createdAt := now.Add(-20 * time.Minute)

	due, err := isScheduleDue("5m", nil, createdAt, now)
	if err != nil {
		t.Fatalf("isScheduleDue interval: %v", err)
	}
	if !due {
		t.Fatal("expected job to be due when never run and created > interval ago")
	}

	last := now.Add(-2 * time.Minute)
	due, err = isScheduleDue("5m", &last, createdAt, now)
	if err != nil {
		t.Fatalf("isScheduleDue interval with last run: %v", err)
	}
	if due {
		t.Fatal("expected job not due when last run is too recent")
	}
}

func TestIsScheduleDueCron(t *testing.T) {
	createdAt := time.Date(2026, 2, 28, 8, 0, 0, 0, time.UTC)
	last := time.Date(2026, 2, 28, 8, 5, 0, 0, time.UTC)

	nowNotDue := time.Date(2026, 2, 28, 8, 9, 59, 0, time.UTC)
	due, err := isScheduleDue("*/5 * * * *", &last, createdAt, nowNotDue)
	if err != nil {
		t.Fatalf("isScheduleDue cron not due: %v", err)
	}
	if due {
		t.Fatal("expected cron schedule not due before next window")
	}

	nowDue := time.Date(2026, 2, 28, 8, 10, 0, 0, time.UTC)
	due, err = isScheduleDue("*/5 * * * *", &last, createdAt, nowDue)
	if err != nil {
		t.Fatalf("isScheduleDue cron due: %v", err)
	}
	if !due {
		t.Fatal("expected cron schedule to be due at next matching minute")
	}
}

func TestSchedulerTriggerNowRecordsRun(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{
		sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
			if probeID != "probe-1" {
				return fmt.Errorf("unexpected probe id: %s", probeID)
			}
			if msgType != protocol.MsgCommand {
				return fmt.Errorf("unexpected msg type: %s", msgType)
			}
			cmd, ok := payload.(protocol.CommandPayload)
			if !ok {
				return fmt.Errorf("unexpected payload type %T", payload)
			}
			go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{
				RequestID: cmd.RequestID,
				ExitCode:  0,
				Stdout:    "ok",
			})
			return nil
		},
	}

	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())

	job, err := store.CreateJob(Job{
		Name:     "test run",
		Command:  "echo hi",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		Enabled:  false,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 50)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) == 1 && runs[0].Status == RunStatusSuccess {
			if runs[0].ExitCode == nil || *runs[0].ExitCode != 0 {
				t.Fatalf("expected exit code 0, got %#v", runs[0].ExitCode)
			}
			if runs[0].AdmissionDecision != string(AdmissionOutcomeAllow) {
				t.Fatalf("expected admission decision allow, got %q", runs[0].AdmissionDecision)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	runs, _ := store.ListRunsByJob(job.ID, 50)
	t.Fatalf("expected successful run to be recorded, got %#v", runs)
}

func TestSchedulerEmitsRunLifecycleCorrelationMetadata(t *testing.T) {
	store := newTestStore(t)

	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{
		sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
			cmd, ok := payload.(protocol.CommandPayload)
			if !ok {
				return fmt.Errorf("unexpected payload type %T", payload)
			}
			go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 0, Stdout: "ok"})
			return nil
		},
	}

	var (
		emitMu sync.Mutex
		emits  []LifecycleEvent
	)
	scheduler := NewScheduler(
		store,
		sender,
		fleetMgr,
		tracker,
		zap.NewNop(),
		WithLifecycleObserver(LifecycleObserverFunc(func(event LifecycleEvent) {
			emitMu.Lock()
			emits = append(emits, event)
			emitMu.Unlock()
		})),
	)

	job, err := store.CreateJob(Job{Name: "emit-run", Command: "echo ok", Schedule: "1h", Target: Target{Kind: TargetKindProbe, Value: "probe-1"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	waitForLifecycleEvent(t, &emitMu, &emits, EventJobRunSucceeded, 2*time.Second)

	emitMu.Lock()
	eventsCopy := append([]LifecycleEvent(nil), emits...)
	emitMu.Unlock()

	queued := findLifecycleEvent(eventsCopy, EventJobRunQueued)
	started := findLifecycleEvent(eventsCopy, EventJobRunStarted)
	succeeded := findLifecycleEvent(eventsCopy, EventJobRunSucceeded)
	if queued == nil || started == nil || succeeded == nil {
		t.Fatalf("expected queued/started/succeeded events, got %+v", eventsCopy)
	}

	for _, evt := range []*LifecycleEvent{queued, started, succeeded} {
		if evt.JobID != job.ID {
			t.Fatalf("event %s job_id=%q want %q", evt.Type, evt.JobID, job.ID)
		}
		if evt.RunID == "" || evt.ExecutionID == "" || evt.ProbeID == "" || evt.RequestID == "" {
			t.Fatalf("event %s missing correlation metadata: %+v", evt.Type, evt)
		}
		if evt.Attempt != 1 || evt.MaxAttempts != 1 {
			t.Fatalf("event %s attempt metadata invalid: %+v", evt.Type, evt)
		}
	}
}

func TestSchedulerEmitsRetryAndCanceledLifecycleEvents(t *testing.T) {
	store := newTestStore(t)
	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	attempts := 0
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
		cmd := payload.(protocol.CommandPayload)
		attempts++
		if attempts == 1 {
			go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 1, Stderr: "boom"})
			return nil
		}
		return nil
	}}

	var (
		emitMu sync.Mutex
		emits  []LifecycleEvent
	)
	scheduler := NewScheduler(
		store,
		sender,
		fleetMgr,
		tracker,
		zap.NewNop(),
		WithLifecycleObserver(LifecycleObserverFunc(func(event LifecycleEvent) {
			emitMu.Lock()
			emits = append(emits, event)
			emitMu.Unlock()
		})),
	)

	job, err := store.CreateJob(Job{
		Name:     "emit-retry-cancel",
		Command:  "false",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		RetryPolicy: &RetryPolicy{
			MaxAttempts:    2,
			InitialBackoff: "30ms",
			Multiplier:     2,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	waitForLifecycleEvent(t, &emitMu, &emits, EventJobRunRetryScheduled, 2*time.Second)
	waitForLifecycleCondition(t, &emitMu, &emits, 2*time.Second, func(events []LifecycleEvent) bool {
		for _, event := range events {
			if event.Type == EventJobRunQueued && event.Attempt == 2 {
				return true
			}
		}
		return false
	})
	if _, err := scheduler.CancelJob(job.ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	waitForLifecycleEvent(t, &emitMu, &emits, EventJobRunCanceled, 2*time.Second)

	emitMu.Lock()
	eventsCopy := append([]LifecycleEvent(nil), emits...)
	emitMu.Unlock()

	failed := findLifecycleEvent(eventsCopy, EventJobRunFailed)
	retry := findLifecycleEvent(eventsCopy, EventJobRunRetryScheduled)
	canceled := findLifecycleEvent(eventsCopy, EventJobRunCanceled)
	if failed == nil || retry == nil || canceled == nil {
		t.Fatalf("expected failed/retry/canceled events, got %+v", eventsCopy)
	}

	for _, evt := range []*LifecycleEvent{failed, retry, canceled} {
		if evt.JobID != job.ID {
			t.Fatalf("event %s job_id=%q want %q", evt.Type, evt.JobID, job.ID)
		}
		if evt.RunID == "" || evt.ExecutionID == "" || evt.ProbeID == "" || evt.RequestID == "" {
			t.Fatalf("event %s missing correlation metadata: %+v", evt.Type, evt)
		}
		if evt.Attempt <= 0 || evt.MaxAttempts != 2 {
			t.Fatalf("event %s attempt metadata invalid: %+v", evt.Type, evt)
		}
	}
}

func TestSchedulerSkipsOverlappingRunsForSameTarget(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error { return nil }}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())

	job, err := store.CreateJob(Job{
		Name:     "overlap",
		Command:  "echo overlap",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("second trigger: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 50)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) > 0 {
			if len(runs) != 1 {
				t.Fatalf("expected one in-flight run, got %d", len(runs))
			}
			if runs[0].Status != RunStatusRunning && runs[0].Status != RunStatusPending {
				t.Fatalf("expected active status while pending, got %s", runs[0].Status)
			}
			tracker.complete(runs[0].RequestID, &protocol.CommandResultPayload{RequestID: runs[0].RequestID, ExitCode: 0, Stdout: "ok"})
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected at least one run to be recorded")
}

func TestSchedulerCancelJobMarksRunCanceledAndIgnoresLateResult(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error { return nil }}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())

	job, err := store.CreateJob(Job{
		Name:     "cancel-race",
		Command:  "echo slow",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	var run JobRun
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 10)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) > 0 {
			run = runs[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.ID == "" {
		t.Fatal("expected run to exist")
	}

	summary, err := scheduler.CancelJob(job.ID)
	if err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	if summary.CanceledRuns < 1 {
		t.Fatalf("expected canceled runs >= 1, got %+v", summary)
	}

	tracker.complete(run.RequestID, &protocol.CommandResultPayload{RequestID: run.RequestID, ExitCode: 0, Stdout: "ok"})

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.GetRun(run.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if updated.Status == RunStatusCanceled {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	updated, _ := store.GetRun(run.ID)
	t.Fatalf("expected canceled status after cancel race, got %#v", updated)
}

func TestSchedulerRetriesWithBackoffAndCap(t *testing.T) {
	store := newTestStore(t)
	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	var (
		mu            sync.Mutex
		dispatchTimes []time.Time
	)
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
		cmd, ok := payload.(protocol.CommandPayload)
		if !ok {
			return fmt.Errorf("unexpected payload type %T", payload)
		}
		mu.Lock()
		dispatchTimes = append(dispatchTimes, time.Now())
		mu.Unlock()
		go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 1, Stderr: "boom"})
		return nil
	}}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())

	job, err := store.CreateJob(Job{
		Name:     "retry-backoff",
		Command:  "false",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		RetryPolicy: &RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: "20ms",
			Multiplier:     3,
			MaxBackoff:     "40ms",
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	waitForRuns(t, store, job.ID, 3, 3*time.Second)
	runs, err := store.ListRunsByJob(job.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(runs))
	}

	byAttempt := make(map[int]JobRun, len(runs))
	for _, run := range runs {
		byAttempt[run.Attempt] = run
		if run.MaxAttempts != 3 {
			t.Fatalf("run attempt %d max_attempts=%d want 3", run.Attempt, run.MaxAttempts)
		}
		if run.Status != RunStatusFailed {
			t.Fatalf("run attempt %d status=%s want failed", run.Attempt, run.Status)
		}
	}
	if byAttempt[1].RetryScheduledAt == nil || byAttempt[2].RetryScheduledAt == nil {
		t.Fatalf("expected retry_scheduled_at for attempts 1 and 2, got %#v", byAttempt)
	}
	if byAttempt[3].RetryScheduledAt != nil {
		t.Fatalf("expected final attempt to have no retry_scheduled_at, got %v", byAttempt[3].RetryScheduledAt)
	}

	mu.Lock()
	timesCopy := append([]time.Time(nil), dispatchTimes...)
	mu.Unlock()
	if len(timesCopy) != 3 {
		t.Fatalf("expected 3 dispatches, got %d", len(timesCopy))
	}
	delay1 := timesCopy[1].Sub(timesCopy[0])
	delay2 := timesCopy[2].Sub(timesCopy[1])
	if delay1 < 15*time.Millisecond {
		t.Fatalf("first retry delay too short: %s", delay1)
	}
	if delay2 < 30*time.Millisecond {
		t.Fatalf("second retry delay too short (cap expected): %s", delay2)
	}
}

func TestSchedulerNoRetryAfterSuccess(t *testing.T) {
	store := newTestStore(t)
	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
		cmd := payload.(protocol.CommandPayload)
		go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 0, Stdout: "ok"})
		return nil
	}}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())

	job, err := store.CreateJob(Job{
		Name:     "retry-success",
		Command:  "echo ok",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		RetryPolicy: &RetryPolicy{
			MaxAttempts:    4,
			InitialBackoff: "20ms",
			Multiplier:     2,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	waitForRuns(t, store, job.ID, 1, 2*time.Second)
	time.Sleep(120 * time.Millisecond)
	runs, err := store.ListRunsByJob(job.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run for successful execution, got %d", len(runs))
	}
	if runs[0].Status != RunStatusSuccess {
		t.Fatalf("expected success status, got %s", runs[0].Status)
	}
}

func TestSchedulerCancelJobStopsScheduledRetry(t *testing.T) {
	store := newTestStore(t)
	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
		cmd := payload.(protocol.CommandPayload)
		go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 1, Stderr: "boom"})
		return nil
	}}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())

	job, err := store.CreateJob(Job{
		Name:     "cancel-retry",
		Command:  "false",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		RetryPolicy: &RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: "250ms",
			Multiplier:     2,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 10)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) >= 1 && runs[0].Status == RunStatusFailed && runs[0].RetryScheduledAt != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	summary, err := scheduler.CancelJob(job.ID)
	if err != nil {
		t.Fatalf("cancel job: %v", err)
	}
	if summary.CanceledRetries < 1 {
		t.Fatalf("expected at least one canceled scheduled retry, got %+v", summary)
	}

	time.Sleep(400 * time.Millisecond)
	runs, err := store.ListRunsByJob(job.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected queued retry to be canceled; got %d runs", len(runs))
	}
}

func TestSchedulerRetriesOnDispatchFailure(t *testing.T) {
	store := newTestStore(t)
	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	attempts := 0
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
		attempts++
		cmd := payload.(protocol.CommandPayload)
		if attempts < 3 {
			return fmt.Errorf("probe offline")
		}
		go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 0, Stdout: "ok"})
		return nil
	}}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())

	job, err := store.CreateJob(Job{
		Name:     "dispatch-retries",
		Command:  "echo ok",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		RetryPolicy: &RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: "20ms",
			Multiplier:     2,
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 10)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) == 3 {
			var foundSuccess bool
			for _, run := range runs {
				if run.Status == RunStatusSuccess {
					foundSuccess = true
				}
			}
			if foundSuccess {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	runs, _ := store.ListRunsByJob(job.ID, 10)
	t.Fatalf("expected success after dispatch retries, got %#v", runs)
}

func TestSchedulerAdmissionDenyRecordsDeniedRun(t *testing.T) {
	store := newTestStore(t)
	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	var sent int
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
		sent++
		return nil
	}}

	scheduler := NewScheduler(
		store,
		sender,
		fleetMgr,
		tracker,
		zap.NewNop(),
		WithAdmissionEvaluator(JobAdmissionEvaluatorFunc(func(ctx context.Context, job Job, probeID string) JobAdmissionDecision {
			return JobAdmissionDecision{
				Outcome:   AdmissionOutcomeDeny,
				Reason:    "capacity degraded",
				Rationale: map[string]any{"policy": "capacity-policy-v1", "availability": "degraded"},
			}
		})),
	)

	job, err := store.CreateJob(Job{Name: "admission-deny", Command: "echo no", Schedule: "1h", Target: Target{Kind: TargetKindProbe, Value: "probe-1"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 5)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) == 1 {
			run := runs[0]
			if run.Status != RunStatusDenied {
				t.Fatalf("expected denied status, got %s", run.Status)
			}
			if run.AdmissionDecision != string(AdmissionOutcomeDeny) {
				t.Fatalf("expected deny admission decision, got %q", run.AdmissionDecision)
			}
			if run.AdmissionReason == "" || run.Output == "" {
				t.Fatalf("expected denial rationale/output, got %+v", run)
			}
			if sent != 0 {
				t.Fatalf("expected no dispatches on denied run, got %d", sent)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	runs, _ := store.ListRunsByJob(job.ID, 5)
	t.Fatalf("expected denied run to be recorded, got %#v", runs)
}

func TestSchedulerAdmissionQueueReevaluatesAndDispatches(t *testing.T) {
	store := newTestStore(t)
	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error {
		cmd, ok := payload.(protocol.CommandPayload)
		if !ok {
			return fmt.Errorf("unexpected payload type %T", payload)
		}
		go tracker.complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 0, Stdout: "ok"})
		return nil
	}}

	var (
		admissionChecks int
		admissionMu     sync.Mutex
	)
	scheduler := NewScheduler(
		store,
		sender,
		fleetMgr,
		tracker,
		zap.NewNop(),
		WithAdmissionRetryDelay(15*time.Millisecond),
		WithAdmissionEvaluator(JobAdmissionEvaluatorFunc(func(ctx context.Context, job Job, probeID string) JobAdmissionDecision {
			admissionMu.Lock()
			admissionChecks++
			check := admissionChecks
			admissionMu.Unlock()
			if check == 1 {
				return JobAdmissionDecision{
					Outcome:   AdmissionOutcomeQueue,
					Reason:    "capacity limited",
					RetryAfter: 15 * time.Millisecond,
					Rationale: map[string]any{"availability": "limited"},
				}
			}
			return JobAdmissionDecision{
				Outcome:   AdmissionOutcomeAllow,
				Reason:    "capacity recovered",
				Rationale: map[string]any{"availability": "normal"},
			}
		})),
	)

	job, err := store.CreateJob(Job{Name: "admission-queue", Command: "echo ok", Schedule: "1h", Target: Target{Kind: TargetKindProbe, Value: "probe-1"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 5)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) == 1 && runs[0].Status == RunStatusSuccess {
			run := runs[0]
			if run.Attempt != 1 {
				t.Fatalf("expected one attempt after deferred admission, got %d", run.Attempt)
			}
			if run.AdmissionDecision != string(AdmissionOutcomeAllow) {
				t.Fatalf("expected final admission decision allow, got %q", run.AdmissionDecision)
			}
			admissionMu.Lock()
			checks := admissionChecks
			admissionMu.Unlock()
			if checks < 2 {
				t.Fatalf("expected queued admission to re-evaluate, checks=%d", checks)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	runs, _ := store.ListRunsByJob(job.ID, 5)
	t.Fatalf("expected queued admission to drain to success, got %#v", runs)
}

func waitForRuns(t *testing.T, store *Store, jobID string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(jobID, 50)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) == want {
			allTerminal := true
			for _, run := range runs {
				if run.Status == RunStatusQueued || run.Status == RunStatusPending || run.Status == RunStatusRunning {
					allTerminal = false
					break
				}
			}
			if allTerminal {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	runs, _ := store.ListRunsByJob(jobID, 50)
	t.Fatalf("timed out waiting for %d runs, got %#v", want, runs)
}

func waitForLifecycleEvent(t *testing.T, mu *sync.Mutex, events *[]LifecycleEvent, want LifecycleEventType, timeout time.Duration) {
	t.Helper()
	waitForLifecycleCondition(t, mu, events, timeout, func(events []LifecycleEvent) bool {
		return findLifecycleEvent(events, want) != nil
	})
}

func waitForLifecycleCondition(t *testing.T, mu *sync.Mutex, events *[]LifecycleEvent, timeout time.Duration, predicate func([]LifecycleEvent) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		copyEvents := append([]LifecycleEvent(nil), (*events)...)
		mu.Unlock()
		if predicate(copyEvents) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	copyEvents := append([]LifecycleEvent(nil), (*events)...)
	mu.Unlock()
	t.Fatalf("timed out waiting for lifecycle condition, got %+v", copyEvents)
}

func findLifecycleEvent(events []LifecycleEvent, want LifecycleEventType) *LifecycleEvent {
	for i := range events {
		if events[i].Type == want {
			return &events[i]
		}
	}
	return nil
}

type fakeSender struct {
	sendFn func(probeID string, msgType protocol.MessageType, payload any) error
}

func (f *fakeSender) SendTo(probeID string, msgType protocol.MessageType, payload any) error {
	return f.sendFn(probeID, msgType, payload)
}

type fakeTracker struct {
	mu      sync.Mutex
	pending map[string]*cmdtracker.PendingCommand
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{pending: make(map[string]*cmdtracker.PendingCommand)}
}

func (f *fakeTracker) Track(requestID, probeID, command string, level protocol.CapabilityLevel) *cmdtracker.PendingCommand {
	pc := &cmdtracker.PendingCommand{
		RequestID: requestID,
		ProbeID:   probeID,
		Command:   command,
		Level:     level,
		Submitted: time.Now().UTC(),
		Result:    make(chan *protocol.CommandResultPayload, 1),
	}
	f.mu.Lock()
	f.pending[requestID] = pc
	f.mu.Unlock()
	return pc
}

func (f *fakeTracker) Cancel(requestID string) {
	f.mu.Lock()
	pc, ok := f.pending[requestID]
	if ok {
		delete(f.pending, requestID)
		close(pc.Result)
	}
	f.mu.Unlock()
}

func (f *fakeTracker) complete(requestID string, payload *protocol.CommandResultPayload) {
	f.mu.Lock()
	pc, ok := f.pending[requestID]
	if ok {
		delete(f.pending, requestID)
	}
	f.mu.Unlock()
	if ok {
		pc.Result <- payload
	}
}
