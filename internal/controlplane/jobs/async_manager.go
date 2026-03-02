package jobs

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

type AsyncManagerOption func(*AsyncManager)

// WithAsyncMaxQueueDepth rejects new queued async jobs when the queued depth is
// already at or above the configured maximum.
func WithAsyncMaxQueueDepth(maxDepth int) AsyncManagerOption {
	return func(m *AsyncManager) {
		if maxDepth > 0 {
			m.maxQueueDepth = maxDepth
		}
	}
}

// AsyncManager orchestrates async job lifecycle updates on top of Store.
type AsyncManager struct {
	store *Store

	mu            sync.Mutex
	maxQueueDepth int
}

func NewAsyncManager(store *Store, opts ...AsyncManagerOption) *AsyncManager {
	m := &AsyncManager{store: store}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

func (m *AsyncManager) CreateJob(job AsyncJob) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	job.State = AsyncJobStateQueued

	if m.maxQueueDepth > 0 {
		m.mu.Lock()
		defer m.mu.Unlock()

		queued, err := m.store.CountAsyncJobsByState(AsyncJobStateQueued)
		if err != nil {
			return nil, err
		}
		if queued >= m.maxQueueDepth {
			return nil, &AsyncQueueSaturatedError{Queued: queued, MaxDepth: m.maxQueueDepth}
		}
	}

	return m.store.CreateAsyncJob(job)
}

func (m *AsyncManager) CreateForCommand(probeID string, cmd protocol.CommandPayload) (*AsyncJob, error) {
	job := AsyncJob{
		ProbeID:   strings.TrimSpace(probeID),
		RequestID: strings.TrimSpace(cmd.RequestID),
		Command:   strings.TrimSpace(cmd.Command),
		Args:      append([]string(nil), cmd.Args...),
		Level:     string(cmd.Level),
	}
	return m.CreateJob(job)
}

func (m *AsyncManager) ListJobs(limit int) ([]AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	return m.store.ListAsyncJobs(limit)
}

func (m *AsyncManager) GetJob(id string) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	return m.store.GetAsyncJob(id)
}

func (m *AsyncManager) MarkRunning(jobID string) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	return m.store.TransitionAsyncJob(jobID, AsyncJobStateRunning, AsyncJobTransitionOptions{})
}

func (m *AsyncManager) MarkRunningByRequestID(requestID string) (*AsyncJob, error) {
	return m.transitionByRequestID(requestID, AsyncJobStateRunning, AsyncJobTransitionOptions{})
}

func (m *AsyncManager) MarkWaitingApproval(jobID, approvalID, reason string, expiresAt *time.Time) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	return m.store.TransitionAsyncJob(jobID, AsyncJobStateWaitingApproval, AsyncJobTransitionOptions{
		StatusReason: reason,
		ApprovalID:   approvalID,
		ExpiresAt:    expiresAt,
	})
}

func (m *AsyncManager) MarkSucceededByRequestID(requestID string, exitCode int, output string) (*AsyncJob, error) {
	return m.transitionByRequestID(requestID, AsyncJobStateSucceeded, AsyncJobTransitionOptions{
		ExitCode: &exitCode,
		Output:   output,
	})
}

func (m *AsyncManager) MarkFailedByRequestID(requestID, reason, output string, exitCode *int) (*AsyncJob, error) {
	return m.transitionByRequestID(requestID, AsyncJobStateFailed, AsyncJobTransitionOptions{
		StatusReason: reason,
		ExitCode:     exitCode,
		Output:       output,
	})
}

func (m *AsyncManager) CancelJob(jobID, reason string) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	return m.store.CancelAsyncJob(jobID, reason)
}

func (m *AsyncManager) ExpireStale(now time.Time) (runningExpired int, approvalsExpired int, err error) {
	if m == nil || m.store == nil {
		return 0, 0, fmt.Errorf("async manager unavailable")
	}
	runningExpired, err = m.store.ExpireRunningAsyncJobs("control plane restarted before command completed")
	if err != nil {
		return 0, 0, err
	}
	approvalsExpired, err = m.store.ExpireWaitingApprovalAsyncJobs(now, "approval window expired while control plane restarted")
	if err != nil {
		return runningExpired, 0, err
	}
	return runningExpired, approvalsExpired, nil
}

func (m *AsyncManager) transitionByRequestID(requestID string, to AsyncJobState, opts AsyncJobTransitionOptions) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	job, err := m.store.GetAsyncJobByRequestID(requestID)
	if err != nil {
		return nil, err
	}
	return m.store.TransitionAsyncJob(job.ID, to, opts)
}

// ApproveJob atomically transitions a waiting_approval job to running.
// Uses an atomic UPDATE WHERE state=waiting_approval so concurrent calls return ErrAsyncJobConflict.
func (m *AsyncManager) ApproveJob(jobID string) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id required")
	}
	return m.store.ApproveAsyncJob(jobID)
}

// RejectJob atomically transitions a waiting_approval job to failed with the given reason.
// Uses an atomic UPDATE WHERE state=waiting_approval so concurrent calls return ErrAsyncJobConflict.
func (m *AsyncManager) RejectJob(jobID, reason string) (*AsyncJob, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("async manager unavailable")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id required")
	}
	return m.store.RejectAsyncJob(jobID, strings.TrimSpace(reason))
}
