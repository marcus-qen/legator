package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type drillRuntime struct {
	mu     sync.Mutex
	seq    int
	byID   map[string]*drillRuntimeEntry
	byName map[string]string
}

type drillRuntimeEntry struct {
	id   string
	name string

	done     chan struct{}
	exitCode int
	once     sync.Once
}

func newDrillRuntime() *drillRuntime {
	return &drillRuntime{
		byID:   make(map[string]*drillRuntimeEntry),
		byName: make(map[string]string),
	}
}

func (r *drillRuntime) Run(_ context.Context, req ContainerRunRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	id := fmt.Sprintf("drill-ctr-%d", r.seq)
	entry := &drillRuntimeEntry{
		id:   id,
		name: req.Name,
		done: make(chan struct{}),
	}
	r.byID[id] = entry
	r.byName[req.Name] = id
	return id, nil
}

func (r *drillRuntime) Stop(_ context.Context, ref string) error {
	return r.finish(ref, 143)
}

func (r *drillRuntime) Remove(_ context.Context, ref string, force bool) error {
	r.mu.Lock()
	id, entry, ok := r.lookupLocked(ref)
	if !ok {
		r.mu.Unlock()
		return errors.New("no such container")
	}
	delete(r.byID, id)
	delete(r.byName, entry.name)
	r.mu.Unlock()

	exitCode := 0
	if force {
		exitCode = 137
	}
	entry.finish(exitCode)
	return nil
}

func (r *drillRuntime) Wait(ctx context.Context, ref string) (int, error) {
	r.mu.Lock()
	_, entry, ok := r.lookupLocked(ref)
	r.mu.Unlock()
	if !ok {
		return 0, errors.New("no such container")
	}

	select {
	case <-entry.done:
		return entry.exitCode, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (r *drillRuntime) Crash(ref string) error {
	return r.finish(ref, 137)
}

func (r *drillRuntime) ArtifactCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

func (r *drillRuntime) finish(ref string, code int) error {
	r.mu.Lock()
	_, entry, ok := r.lookupLocked(ref)
	r.mu.Unlock()
	if !ok {
		return errors.New("no such container")
	}
	entry.finish(code)
	return nil
}

func (r *drillRuntime) lookupLocked(ref string) (string, *drillRuntimeEntry, bool) {
	if entry, ok := r.byID[ref]; ok {
		return ref, entry, true
	}
	if id, ok := r.byName[ref]; ok {
		entry, ok := r.byID[id]
		if ok {
			return id, entry, true
		}
	}
	return "", nil, false
}

func (e *drillRuntimeEntry) finish(code int) {
	e.once.Do(func() {
		e.exitCode = code
		close(e.done)
	})
}

func TestFailureDrill_RunnerCrashDuringActiveJobCleansUp(t *testing.T) {
	rt := newDrillRuntime()
	events := make(chan BackendEvent, 8)
	backend := NewContainerBackend(ContainerBackendConfig{
		Runtime:        rt,
		DefaultTimeout: 5 * time.Second,
		EventSink: func(evt BackendEvent) {
			events <- evt
		},
	})

	runnerID := "runner-drill-crash"
	started, err := backend.Start(context.Background(), StartExecutionRequest{
		RunnerID: runnerID,
		JobID:    "job-drill-crash",
		Command:  []string{"sleep", "120"},
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() {
		_ = backend.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: runnerID, Reason: "test_cleanup"})
	})

	if err := rt.Crash(started.ContainerID); err != nil {
		t.Fatalf("force crash: %v", err)
	}

	seenError := false
	seenTeardown := false
	deadline := time.After(2 * time.Second)
	for !seenError || !seenTeardown {
		select {
		case evt := <-events:
			switch evt.Type {
			case BackendEventCommandError:
				seenError = true
			case BackendEventTeardown:
				seenTeardown = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for crash drill events: command_error=%v teardown=%v", seenError, seenTeardown)
		}
	}

	waitForRunnerCleanup(t, backend, rt, runnerID)
}

func TestFailureDrill_TeardownLeakCheckRemovesOrphanArtifacts(t *testing.T) {
	rt := newDrillRuntime()
	backend := NewContainerBackend(ContainerBackendConfig{
		Runtime:        rt,
		DefaultTimeout: 5 * time.Second,
	})

	runnerID := "runner-drill-leak"
	if _, err := backend.Start(context.Background(), StartExecutionRequest{
		RunnerID: runnerID,
		JobID:    "job-drill-leak",
		Command:  []string{"sleep", "120"},
		Timeout:  5 * time.Second,
	}); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() {
		_ = backend.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: runnerID, Reason: "test_cleanup"})
	})

	backend.mu.Lock()
	delete(backend.running, runnerID)
	backend.mu.Unlock()

	if err := backend.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: runnerID, Reason: "leak_check"}); err != nil {
		t.Fatalf("teardown leak check: %v", err)
	}

	waitForRunnerCleanup(t, backend, rt, runnerID)
}

func waitForRunnerCleanup(t *testing.T, backend *ContainerBackend, rt *drillRuntime, runnerID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		backend.mu.Lock()
		_, tracked := backend.running[runnerID]
		backend.mu.Unlock()
		if !tracked && rt.ArtifactCount() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	backend.mu.Lock()
	_, tracked := backend.running[runnerID]
	backend.mu.Unlock()
	t.Fatalf("orphaned runner artifacts remain: tracked=%v artifacts=%d", tracked, rt.ArtifactCount())
}
