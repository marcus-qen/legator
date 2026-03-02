package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultSandboxImage = "docker.io/library/alpine:3.20"

// ContainerRuntime abstracts rootless container operations.
type ContainerRuntime interface {
	Run(context.Context, ContainerRunRequest) (string, error)
	Stop(context.Context, string) error
	Remove(context.Context, string, bool) error
	Wait(context.Context, string) (int, error)
}

// ContainerRunRequest is the low-level runtime contract.
type ContainerRunRequest struct {
	Name    string
	Image   string
	Command []string
	Labels  map[string]string
}

// ContainerBackendConfig wires defaults and runtime adapters.
type ContainerBackendConfig struct {
	Runtime        ContainerRuntime
	RuntimeCommand string
	DefaultImage   string
	DefaultTimeout time.Duration
	Now            func() time.Time
	EventSink      func(BackendEvent)
}

// ContainerBackend executes runner contracts using disposable rootless containers.
type ContainerBackend struct {
	runtime        ContainerRuntime
	defaultImage   string
	defaultTimeout time.Duration
	now            func() time.Time
	eventSink      func(BackendEvent)

	mu      sync.Mutex
	running map[string]*runningContainer
}

type runningContainer struct {
	runnerID      string
	jobID         string
	containerID   string
	containerName string
	cancelWait    context.CancelFunc
}

// NewContainerBackend creates a rootless container execution backend.
func NewContainerBackend(cfg ContainerBackendConfig) *ContainerBackend {
	runtime := cfg.Runtime
	if runtime == nil {
		runtime = NewPodmanRuntime(strings.TrimSpace(cfg.RuntimeCommand))
	}
	image := strings.TrimSpace(cfg.DefaultImage)
	if image == "" {
		image = defaultSandboxImage
	}
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	eventSink := cfg.EventSink
	if eventSink == nil {
		eventSink = func(BackendEvent) {}
	}
	return &ContainerBackend{
		runtime:        runtime,
		defaultImage:   image,
		defaultTimeout: timeout,
		now:            nowFn,
		eventSink:      eventSink,
		running:        make(map[string]*runningContainer),
	}
}

// Start runs a disposable rootless container and tracks lifecycle for cleanup.
func (b *ContainerBackend) Start(ctx context.Context, req StartExecutionRequest) (*StartExecutionResult, error) {
	runnerID := strings.TrimSpace(req.RunnerID)
	if runnerID == "" {
		return nil, ErrRunnerIDRequired
	}
	command := compactCommand(req.Command)
	if len(command) == 0 {
		return nil, ErrSandboxCommandRequired
	}
	image := strings.TrimSpace(req.Image)
	if image == "" {
		image = b.defaultImage
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = b.defaultTimeout
	}
	jobID := strings.TrimSpace(req.JobID)
	containerName := containerNameForRunner(runnerID)

	// Best-effort orphan cleanup before attempting a fresh run.
	_ = b.runtime.Remove(ctx, containerName, true)

	containerID, err := b.runtime.Run(ctx, ContainerRunRequest{
		Name:    containerName,
		Image:   image,
		Command: command,
		Labels: map[string]string{
			"io.legator.runner_id": runnerID,
			"io.legator.job_id":    jobID,
		},
	})
	if err != nil {
		_ = b.runtime.Remove(ctx, containerName, true)
		b.emit(BackendEvent{
			Type:          BackendEventCommandError,
			RunnerID:      runnerID,
			JobID:         jobID,
			ContainerName: containerName,
			Reason:        "runtime_run_failed",
			Err:           err,
		})
		return nil, err
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	entry := &runningContainer{
		runnerID:      runnerID,
		jobID:         jobID,
		containerID:   strings.TrimSpace(containerID),
		containerName: containerName,
		cancelWait:    cancel,
	}

	b.mu.Lock()
	if prev, ok := b.running[runnerID]; ok {
		b.mu.Unlock()
		_ = b.runtime.Remove(ctx, entry.containerID, true)
		prev.cancelWait()
		return nil, fmt.Errorf("runner execution already active for %s", runnerID)
	}
	b.running[runnerID] = entry
	b.mu.Unlock()

	b.emit(BackendEvent{
		Type:          BackendEventStarted,
		RunnerID:      runnerID,
		JobID:         jobID,
		ContainerID:   entry.containerID,
		ContainerName: containerName,
		Reason:        "start",
	})

	go b.monitor(waitCtx, entry)

	return &StartExecutionResult{ContainerID: entry.containerID, ContainerName: containerName}, nil
}

func (b *ContainerBackend) monitor(waitCtx context.Context, entry *runningContainer) {
	exitCode, err := b.runtime.Wait(waitCtx, entry.containerID)
	switch {
	case err == nil && exitCode == 0:
		_ = b.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: entry.runnerID, Reason: "completed"})
	case err == nil && exitCode != 0:
		b.emit(BackendEvent{
			Type:          BackendEventCommandError,
			RunnerID:      entry.runnerID,
			JobID:         entry.jobID,
			ContainerID:   entry.containerID,
			ContainerName: entry.containerName,
			Reason:        "non_zero_exit",
			Err:           fmt.Errorf("container exited with code %d", exitCode),
		})
		_ = b.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: entry.runnerID, Reason: "failed"})
	case errors.Is(err, context.Canceled):
		// stop/teardown path already owns cleanup.
		return
	case errors.Is(err, context.DeadlineExceeded):
		b.emit(BackendEvent{
			Type:          BackendEventTimeout,
			RunnerID:      entry.runnerID,
			JobID:         entry.jobID,
			ContainerID:   entry.containerID,
			ContainerName: entry.containerName,
			Reason:        "timeout",
			Err:           err,
		})
		_ = b.Stop(context.Background(), StopExecutionRequest{RunnerID: entry.runnerID, Reason: "timeout"})
		_ = b.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: entry.runnerID, Reason: "timeout"})
	default:
		b.emit(BackendEvent{
			Type:          BackendEventCommandError,
			RunnerID:      entry.runnerID,
			JobID:         entry.jobID,
			ContainerID:   entry.containerID,
			ContainerName: entry.containerName,
			Reason:        "wait_failed",
			Err:           err,
		})
		_ = b.Teardown(context.Background(), TeardownExecutionRequest{RunnerID: entry.runnerID, Reason: "wait_failed"})
	}
}

// Stop requests container termination for an active runner.
func (b *ContainerBackend) Stop(ctx context.Context, req StopExecutionRequest) error {
	runnerID := strings.TrimSpace(req.RunnerID)
	if runnerID == "" {
		return ErrRunnerIDRequired
	}
	entry := b.get(runnerID)
	if entry == nil {
		// Best-effort cleanup for stale orphan not currently tracked.
		name := containerNameForRunner(runnerID)
		if err := b.runtime.Stop(ctx, name); err != nil && !isContainerMissing(err) {
			return err
		}
		return nil
	}

	entry.cancelWait()
	if err := b.runtime.Stop(ctx, entry.containerID); err != nil && !isContainerMissing(err) {
		b.emit(BackendEvent{
			Type:          BackendEventCommandError,
			RunnerID:      entry.runnerID,
			JobID:         entry.jobID,
			ContainerID:   entry.containerID,
			ContainerName: entry.containerName,
			Reason:        "stop_failed",
			Err:           err,
		})
		return err
	}
	b.emit(BackendEvent{
		Type:          BackendEventStopped,
		RunnerID:      entry.runnerID,
		JobID:         entry.jobID,
		ContainerID:   entry.containerID,
		ContainerName: entry.containerName,
		Reason:        strings.TrimSpace(req.Reason),
	})
	return nil
}

// Teardown removes tracked container artifacts and forgets local state.
func (b *ContainerBackend) Teardown(ctx context.Context, req TeardownExecutionRequest) error {
	runnerID := strings.TrimSpace(req.RunnerID)
	if runnerID == "" {
		return ErrRunnerIDRequired
	}
	entry := b.take(runnerID)
	name := containerNameForRunner(runnerID)
	if entry != nil {
		entry.cancelWait()
		name = entry.containerName
		if err := b.runtime.Remove(ctx, entry.containerID, true); err != nil && !isContainerMissing(err) {
			b.emit(BackendEvent{
				Type:          BackendEventCommandError,
				RunnerID:      entry.runnerID,
				JobID:         entry.jobID,
				ContainerID:   entry.containerID,
				ContainerName: entry.containerName,
				Reason:        "teardown_failed",
				Err:           err,
			})
			return err
		}
	} else {
		if err := b.runtime.Remove(ctx, name, true); err != nil && !isContainerMissing(err) {
			return err
		}
	}

	b.emit(BackendEvent{
		Type:          BackendEventTeardown,
		RunnerID:      runnerID,
		ContainerName: name,
		Reason:        strings.TrimSpace(req.Reason),
	})
	return nil
}

func (b *ContainerBackend) get(runnerID string) *runningContainer {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.running[runnerID]
	if !ok {
		return nil
	}
	return entry
}

func (b *ContainerBackend) take(runnerID string) *runningContainer {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.running[runnerID]
	if ok {
		delete(b.running, runnerID)
	}
	return entry
}

func (b *ContainerBackend) emit(evt BackendEvent) {
	if evt.At.IsZero() {
		evt.At = b.now()
	}
	b.eventSink(evt)
}

func containerNameForRunner(runnerID string) string {
	norm := strings.NewReplacer(" ", "-", "/", "-", "_", "-").Replace(strings.TrimSpace(runnerID))
	if norm == "" {
		norm = "unknown"
	}
	return "legator-runner-" + strings.ToLower(norm)
}

func compactCommand(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func isContainerMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "container does not exist") ||
		strings.Contains(msg, "no container with name")
}

// PodmanRuntime shells out to podman (rootless by default).
type PodmanRuntime struct {
	command string
}

// NewPodmanRuntime creates a rootless podman runtime adapter.
func NewPodmanRuntime(command string) *PodmanRuntime {
	if strings.TrimSpace(command) == "" {
		command = "podman"
	}
	return &PodmanRuntime{command: strings.TrimSpace(command)}
}

func (p *PodmanRuntime) Run(ctx context.Context, req ContainerRunRequest) (string, error) {
	args := []string{
		"run", "--detach", "--rm",
		"--name", req.Name,
		"--network", "none",
		"--userns", "keep-id",
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
	}
	keys := make([]string, 0, len(req.Labels))
	for k := range req.Labels {
		keys = append(keys, k)
	}
	for _, k := range keys {
		v := strings.TrimSpace(req.Labels[k])
		if v == "" {
			continue
		}
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, req.Image)
	args = append(args, req.Command...)

	stdout, err := p.exec(ctx, args...)
	if err != nil {
		return "", err
	}
	containerID := strings.TrimSpace(stdout)
	if containerID == "" {
		return "", errors.New("podman returned empty container id")
	}
	return containerID, nil
}

func (p *PodmanRuntime) Stop(ctx context.Context, container string) error {
	_, err := p.exec(ctx, "stop", container)
	return err
}

func (p *PodmanRuntime) Remove(ctx context.Context, container string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, container)
	_, err := p.exec(ctx, args...)
	return err
}

func (p *PodmanRuntime) Wait(ctx context.Context, container string) (int, error) {
	stdout, err := p.exec(ctx, "wait", container)
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(strings.Split(stdout, "\n")[0])
	if line == "" {
		return 0, errors.New("podman wait returned empty exit code")
	}
	code, convErr := strconv.Atoi(line)
	if convErr != nil {
		return 0, fmt.Errorf("parse podman wait exit code %q: %w", line, convErr)
	}
	return code, nil
}

func (p *PodmanRuntime) exec(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, p.command, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		trimmed := strings.TrimSpace(stderr.String())
		if trimmed == "" {
			trimmed = strings.TrimSpace(stdout.String())
		}
		if trimmed == "" {
			trimmed = err.Error()
		}
		return "", fmt.Errorf("podman %s: %s", strings.Join(args, " "), trimmed)
	}
	return stdout.String(), nil
}
