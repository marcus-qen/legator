package jobs

import (
	"fmt"
	"strings"
	"time"
)

// LifecycleEventType labels job lifecycle notifications emitted to audit/event surfaces.
type LifecycleEventType string

const (
	EventJobCreated          LifecycleEventType = "job.created"
	EventJobUpdated          LifecycleEventType = "job.updated"
	EventJobDeleted          LifecycleEventType = "job.deleted"
	EventJobRunAdmissionAllowed LifecycleEventType = "job.run.admission_allowed"
	EventJobRunAdmissionQueued  LifecycleEventType = "job.run.admission_queued"
	EventJobRunAdmissionDenied  LifecycleEventType = "job.run.admission_denied"
	EventJobRunQueued           LifecycleEventType = "job.run.queued"
	EventJobRunStarted          LifecycleEventType = "job.run.started"
	EventJobRunRetryScheduled   LifecycleEventType = "job.run.retry_scheduled"
	EventJobRunSucceeded        LifecycleEventType = "job.run.succeeded"
	EventJobRunFailed           LifecycleEventType = "job.run.failed"
	EventJobRunCanceled         LifecycleEventType = "job.run.canceled"
	EventJobRunDenied           LifecycleEventType = "job.run.denied"
)

// LifecycleEvent carries job/run correlation metadata for audit + SSE consumers.
type LifecycleEvent struct {
	Type               LifecycleEventType `json:"type"`
	Timestamp          time.Time          `json:"timestamp"`
	Actor              string             `json:"actor,omitempty"`
	JobID              string             `json:"job_id,omitempty"`
	RunID              string             `json:"run_id,omitempty"`
	ExecutionID        string             `json:"execution_id,omitempty"`
	ProbeID            string             `json:"probe_id,omitempty"`
	Attempt            int                `json:"attempt,omitempty"`
	MaxAttempts        int                `json:"max_attempts,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
	AdmissionDecision  string             `json:"admission_decision,omitempty"`
	AdmissionReason    string             `json:"admission_reason,omitempty"`
	AdmissionRationale any                `json:"admission_rationale,omitempty"`
	DeferredUntil      *time.Time         `json:"deferred_until,omitempty"`
}

// CorrelationMetadata exposes stable correlation keys for audit detail/event payloads.
func (e LifecycleEvent) CorrelationMetadata() map[string]any {
	meta := map[string]any{}
	if id := strings.TrimSpace(e.JobID); id != "" {
		meta["job_id"] = id
	}
	if id := strings.TrimSpace(e.RunID); id != "" {
		meta["run_id"] = id
	}
	if id := strings.TrimSpace(e.ExecutionID); id != "" {
		meta["execution_id"] = id
	}
	if id := strings.TrimSpace(e.ProbeID); id != "" {
		meta["probe_id"] = id
	}
	if e.Attempt > 0 {
		meta["attempt"] = e.Attempt
	}
	if e.MaxAttempts > 0 {
		meta["max_attempts"] = e.MaxAttempts
	}
	if id := strings.TrimSpace(e.RequestID); id != "" {
		meta["request_id"] = id
	}
	if decision := strings.TrimSpace(e.AdmissionDecision); decision != "" {
		meta["admission_decision"] = decision
	}
	if reason := strings.TrimSpace(e.AdmissionReason); reason != "" {
		meta["admission_reason"] = reason
	}
	if e.AdmissionRationale != nil {
		meta["admission_rationale"] = e.AdmissionRationale
	}
	if e.DeferredUntil != nil && !e.DeferredUntil.IsZero() {
		meta["deferred_until"] = e.DeferredUntil.UTC().Format(time.RFC3339Nano)
	}
	return meta
}

// Summary returns a human-readable lifecycle summary reused by audit + SSE streams.
func (e LifecycleEvent) Summary() string {
	target := strings.TrimSpace(e.JobID)
	if target == "" {
		target = "unknown"
	}

	switch e.Type {
	case EventJobCreated:
		return fmt.Sprintf("Job created: %s", target)
	case EventJobUpdated:
		return fmt.Sprintf("Job updated: %s", target)
	case EventJobDeleted:
		return fmt.Sprintf("Job deleted: %s", target)
	case EventJobRunAdmissionAllowed:
		return fmt.Sprintf("Job run admission allowed: %s", target)
	case EventJobRunAdmissionQueued:
		return fmt.Sprintf("Job run admission queued: %s", target)
	case EventJobRunAdmissionDenied:
		return fmt.Sprintf("Job run admission denied: %s", target)
	case EventJobRunQueued:
		return fmt.Sprintf("Job run queued: %s", target)
	case EventJobRunStarted:
		return fmt.Sprintf("Job run started: %s", target)
	case EventJobRunRetryScheduled:
		return fmt.Sprintf("Job run retry scheduled: %s", target)
	case EventJobRunSucceeded:
		return fmt.Sprintf("Job run succeeded: %s", target)
	case EventJobRunFailed:
		return fmt.Sprintf("Job run failed: %s", target)
	case EventJobRunCanceled:
		return fmt.Sprintf("Job run canceled: %s", target)
	case EventJobRunDenied:
		return fmt.Sprintf("Job run denied: %s", target)
	default:
		return fmt.Sprintf("Job event: %s", target)
	}
}

func (e LifecycleEvent) normalize() LifecycleEvent {
	e.Type = LifecycleEventType(strings.TrimSpace(string(e.Type)))
	e.Actor = strings.TrimSpace(e.Actor)
	e.JobID = strings.TrimSpace(e.JobID)
	e.RunID = strings.TrimSpace(e.RunID)
	e.ExecutionID = strings.TrimSpace(e.ExecutionID)
	e.ProbeID = strings.TrimSpace(e.ProbeID)
	e.RequestID = strings.TrimSpace(e.RequestID)
	e.AdmissionDecision = strings.TrimSpace(e.AdmissionDecision)
	e.AdmissionReason = strings.TrimSpace(e.AdmissionReason)
	if e.DeferredUntil != nil {
		ts := e.DeferredUntil.UTC()
		e.DeferredUntil = &ts
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	return e
}

// Normalized returns the event with normalized IDs and a non-zero UTC timestamp.
func (e LifecycleEvent) Normalized() LifecycleEvent {
	return e.normalize()
}

// LifecycleObserver receives normalized lifecycle events.
type LifecycleObserver interface {
	ObserveJobLifecycleEvent(event LifecycleEvent)
}

// LifecycleObserverFunc adapts functions into LifecycleObserver.
type LifecycleObserverFunc func(event LifecycleEvent)

// ObserveJobLifecycleEvent implements LifecycleObserver.
func (fn LifecycleObserverFunc) ObserveJobLifecycleEvent(event LifecycleEvent) {
	if fn != nil {
		fn(event)
	}
}

type noopLifecycleObserver struct{}

func (noopLifecycleObserver) ObserveJobLifecycleEvent(_ LifecycleEvent) {}
