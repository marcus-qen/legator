package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// AsyncJobState captures durable async command lifecycle states.
type AsyncJobState string

const (
	AsyncJobStateQueued          AsyncJobState = "queued"
	AsyncJobStateRunning         AsyncJobState = "running"
	AsyncJobStateWaitingApproval AsyncJobState = "waiting_approval"
	AsyncJobStateSucceeded       AsyncJobState = "succeeded"
	AsyncJobStateFailed          AsyncJobState = "failed"
	AsyncJobStateExpired         AsyncJobState = "expired"
	AsyncJobStateCancelled       AsyncJobState = "cancelled"
)

var (
	ErrInvalidAsyncJobTransition = errors.New("invalid async job transition")
	ErrAsyncQueueSaturated       = errors.New("async queue saturated")
)

// AsyncQueueSaturatedError indicates async admission was rejected because the
// queued depth reached the configured limit.
type AsyncQueueSaturatedError struct {
	Queued   int
	MaxDepth int
}

func (e *AsyncQueueSaturatedError) Error() string {
	if e == nil {
		return ErrAsyncQueueSaturated.Error()
	}
	if e.MaxDepth > 0 {
		return fmt.Sprintf("async queue saturated: queued=%d max_depth=%d", e.Queued, e.MaxDepth)
	}
	return "async queue saturated"
}

func (e *AsyncQueueSaturatedError) Unwrap() error {
	return ErrAsyncQueueSaturated
}

func IsAsyncQueueSaturated(err error) bool {
	return errors.Is(err, ErrAsyncQueueSaturated)
}

// AsyncJob is a durable command execution record.
type AsyncJob struct {
	ID           string        `json:"id"`
	ProbeID      string        `json:"probe_id"`
	RequestID    string        `json:"request_id"`
	Command      string        `json:"command"`
	Args         []string      `json:"args,omitempty"`
	Level        string        `json:"level,omitempty"`
	State        AsyncJobState `json:"state"`
	StatusReason string        `json:"status_reason,omitempty"`
	ApprovalID   string        `json:"approval_id,omitempty"`
	ExitCode     *int          `json:"exit_code,omitempty"`
	Output       string        `json:"output,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	FinishedAt   *time.Time    `json:"finished_at,omitempty"`
	ExpiresAt    *time.Time    `json:"expires_at,omitempty"`
}

// AsyncJobTransitionOptions controls metadata persisted with a transition.
type AsyncJobTransitionOptions struct {
	StatusReason string
	ApprovalID   string
	ExitCode     *int
	Output       string
	StartedAt    *time.Time
	FinishedAt   *time.Time
	ExpiresAt    *time.Time
}

func (s AsyncJobState) IsTerminal() bool {
	switch s {
	case AsyncJobStateSucceeded, AsyncJobStateFailed, AsyncJobStateExpired, AsyncJobStateCancelled:
		return true
	default:
		return false
	}
}

func normalizeAsyncJobState(state AsyncJobState) AsyncJobState {
	return AsyncJobState(strings.TrimSpace(string(state)))
}

func isKnownAsyncJobState(state AsyncJobState) bool {
	switch normalizeAsyncJobState(state) {
	case AsyncJobStateQueued,
		AsyncJobStateRunning,
		AsyncJobStateWaitingApproval,
		AsyncJobStateSucceeded,
		AsyncJobStateFailed,
		AsyncJobStateExpired,
		AsyncJobStateCancelled:
		return true
	default:
		return false
	}
}

func canTransitionAsyncJob(from, to AsyncJobState) bool {
	from = normalizeAsyncJobState(from)
	to = normalizeAsyncJobState(to)
	if from == to {
		return true
	}
	if !isKnownAsyncJobState(from) || !isKnownAsyncJobState(to) {
		return false
	}

	switch from {
	case AsyncJobStateQueued:
		return to == AsyncJobStateRunning || to == AsyncJobStateWaitingApproval || to == AsyncJobStateCancelled || to == AsyncJobStateExpired || to == AsyncJobStateFailed
	case AsyncJobStateWaitingApproval:
		return to == AsyncJobStateRunning || to == AsyncJobStateCancelled || to == AsyncJobStateExpired || to == AsyncJobStateFailed
	case AsyncJobStateRunning:
		return to == AsyncJobStateSucceeded || to == AsyncJobStateFailed || to == AsyncJobStateExpired || to == AsyncJobStateCancelled
	default:
		return false
	}
}
