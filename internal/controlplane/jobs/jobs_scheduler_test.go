package jobs

import (
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
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	runs, _ := store.ListRunsByJob(job.ID, 50)
	t.Fatalf("expected successful run to be recorded, got %#v", runs)
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
