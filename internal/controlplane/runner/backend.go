package runner

import (
	"context"
	"errors"
	"time"
)

var (
	ErrExecutionNotFound = errors.New("runner execution not found")
)

// ExecutionBackend executes disposable runner contracts.
type ExecutionBackend interface {
	Start(context.Context, StartExecutionRequest) (*StartExecutionResult, error)
	Stop(context.Context, StopExecutionRequest) error
	Teardown(context.Context, TeardownExecutionRequest) error
}

// StartExecutionRequest carries the sandbox execution contract.
type StartExecutionRequest struct {
	RunnerID   string
	JobID      string
	SessionID  string
	Image      string
	Command    []string
	Timeout    time.Duration
	Attributes map[string]string
}

// StartExecutionResult captures runtime metadata for an execution.
type StartExecutionResult struct {
	ContainerID   string
	ContainerName string
}

// StopExecutionRequest addresses a running execution.
type StopExecutionRequest struct {
	RunnerID string
	Reason   string
}

// TeardownExecutionRequest removes all runtime artifacts for an execution.
type TeardownExecutionRequest struct {
	RunnerID string
	Reason   string
}

// BackendEventType marks lifecycle notifications from execution backends.
type BackendEventType string

const (
	BackendEventStarted      BackendEventType = "started"
	BackendEventStopped      BackendEventType = "stopped"
	BackendEventTeardown     BackendEventType = "teardown"
	BackendEventCommandError BackendEventType = "command_error"
	BackendEventTimeout      BackendEventType = "timeout"
)

// BackendEvent is emitted by backends for async lifecycle events.
type BackendEvent struct {
	Type          BackendEventType
	RunnerID      string
	JobID         string
	ContainerID   string
	ContainerName string
	Reason        string
	Err           error
	At            time.Time
}
