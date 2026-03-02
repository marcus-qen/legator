package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeContainerRuntime struct {
	mu sync.Mutex

	runs    []ContainerRunRequest
	stops   []string
	removes []string
	waits   []string

	runFn    func(context.Context, ContainerRunRequest) (string, error)
	stopFn   func(context.Context, string) error
	removeFn func(context.Context, string, bool) error
	waitFn   func(context.Context, string) (int, error)
}

func (f *fakeContainerRuntime) Run(ctx context.Context, req ContainerRunRequest) (string, error) {
	f.mu.Lock()
	f.runs = append(f.runs, req)
	f.mu.Unlock()
	if f.runFn != nil {
		return f.runFn(ctx, req)
	}
	return "ctr-1", nil
}

func (f *fakeContainerRuntime) Stop(ctx context.Context, ref string) error {
	f.mu.Lock()
	f.stops = append(f.stops, ref)
	f.mu.Unlock()
	if f.stopFn != nil {
		return f.stopFn(ctx, ref)
	}
	return nil
}

func (f *fakeContainerRuntime) Remove(ctx context.Context, ref string, force bool) error {
	f.mu.Lock()
	f.removes = append(f.removes, ref)
	f.mu.Unlock()
	if f.removeFn != nil {
		return f.removeFn(ctx, ref, force)
	}
	return nil
}

func (f *fakeContainerRuntime) Wait(ctx context.Context, ref string) (int, error) {
	f.mu.Lock()
	f.waits = append(f.waits, ref)
	f.mu.Unlock()
	if f.waitFn != nil {
		return f.waitFn(ctx, ref)
	}
	return 0, nil
}

func TestContainerBackendStopAndTeardown(t *testing.T) {
	rt := &fakeContainerRuntime{}
	backend := NewContainerBackend(ContainerBackendConfig{Runtime: rt, DefaultTimeout: time.Second})

	start, err := backend.Start(context.Background(), StartExecutionRequest{
		RunnerID: "runner-1",
		JobID:    "job-1",
		Image:    "alpine:3.20",
		Command:  []string{"sh", "-lc", "sleep 60"},
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if start.ContainerID == "" {
		t.Fatal("expected container id")
	}

	if err := backend.Stop(context.Background(), StopExecutionRequest{RunnerID: "runner-1", Reason: "test-stop"}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := backend.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: "runner-1", Reason: "test-teardown"}); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.stops) == 0 {
		t.Fatalf("expected stop call")
	}
	if len(rt.removes) == 0 {
		t.Fatalf("expected remove calls")
	}
}

func TestContainerBackendOrphanCleanupOnRunFailure(t *testing.T) {
	rt := &fakeContainerRuntime{}
	rt.runFn = func(context.Context, ContainerRunRequest) (string, error) {
		return "", errors.New("runtime down")
	}

	backend := NewContainerBackend(ContainerBackendConfig{Runtime: rt, DefaultTimeout: time.Second})
	if _, err := backend.Start(context.Background(), StartExecutionRequest{
		RunnerID: "runner-2",
		Command:  []string{"echo", "hello"},
	}); err == nil {
		t.Fatal("expected start error")
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.removes) < 2 {
		t.Fatalf("expected pre-run and failure cleanup removes, got %d", len(rt.removes))
	}
}

func TestContainerBackendTimeoutTriggersStopAndTeardown(t *testing.T) {
	rt := &fakeContainerRuntime{}
	rt.waitFn = func(ctx context.Context, _ string) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}

	events := make(chan BackendEvent, 8)
	backend := NewContainerBackend(ContainerBackendConfig{
		Runtime:        rt,
		DefaultTimeout: 40 * time.Millisecond,
		EventSink: func(evt BackendEvent) {
			events <- evt
		},
	})

	if _, err := backend.Start(context.Background(), StartExecutionRequest{
		RunnerID: "runner-timeout",
		Command:  []string{"sleep", "1000"},
	}); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.After(400 * time.Millisecond)
	seenTimeout := false
	seenTeardown := false
	for !seenTimeout || !seenTeardown {
		select {
		case evt := <-events:
			switch evt.Type {
			case BackendEventTimeout:
				seenTimeout = true
			case BackendEventTeardown:
				seenTeardown = true
			}
		case <-deadline:
			t.Fatalf("timeout waiting for backend events timeout=%v teardown=%v", seenTimeout, seenTeardown)
		}
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.stops) == 0 {
		t.Fatalf("expected timeout path to call stop")
	}
	if len(rt.removes) == 0 {
		t.Fatalf("expected timeout path to call remove")
	}
}

func TestContainerBackendCommandFailureStillTeardown(t *testing.T) {
	rt := &fakeContainerRuntime{}
	rt.waitFn = func(context.Context, string) (int, error) {
		return 17, nil
	}

	events := make(chan BackendEvent, 8)
	backend := NewContainerBackend(ContainerBackendConfig{
		Runtime:        rt,
		DefaultTimeout: time.Second,
		EventSink: func(evt BackendEvent) {
			events <- evt
		},
	})

	if _, err := backend.Start(context.Background(), StartExecutionRequest{
		RunnerID: "runner-failed-cmd",
		Command:  []string{"sh", "-lc", "exit 17"},
	}); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.After(300 * time.Millisecond)
	seenErr := false
	seenTeardown := false
	for !seenErr || !seenTeardown {
		select {
		case evt := <-events:
			switch evt.Type {
			case BackendEventCommandError:
				seenErr = true
			case BackendEventTeardown:
				seenTeardown = true
			}
		case <-deadline:
			t.Fatalf("timeout waiting for failure teardown events command_error=%v teardown=%v", seenErr, seenTeardown)
		}
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.removes) == 0 {
		t.Fatalf("expected remove call on command failure")
	}
}
