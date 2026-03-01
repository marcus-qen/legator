// Package audit provides an append-only audit log for all control plane actions.
// Every command, policy change, approval, and registration is recorded.
package audit

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EventType classifies audit events.
type EventType string

const (
	EventProbeRegistered   EventType = "probe.registered"
	EventProbeOffline      EventType = "probe.offline"
	EventCommandSent       EventType = "command.sent"
	EventCommandResult     EventType = "command.result"
	EventPolicyChanged     EventType = "policy.changed"
	EventApprovalRequest   EventType = "approval.requested"
	EventApprovalDecided   EventType = "approval.decided"
	EventTokenGenerated    EventType = "token.generated"
	EventInventoryUpdate   EventType = "inventory.updated"
	EventFederationRead    EventType = "federation.read"
	EventProbeKeyRotated   EventType = "probe.key_rotated"
	EventProbeDeregistered EventType = "probe.deregistered"
	EventJobCreated            EventType = "job.created"
	EventJobUpdated            EventType = "job.updated"
	EventJobDeleted            EventType = "job.deleted"
	EventJobRunAdmissionAllowed EventType = "job.run.admission_allowed"
	EventJobRunAdmissionQueued  EventType = "job.run.admission_queued"
	EventJobRunAdmissionDenied  EventType = "job.run.admission_denied"
	EventJobRunQueued           EventType = "job.run.queued"
	EventJobRunStarted          EventType = "job.run.started"
	EventJobRunRetryScheduled   EventType = "job.run.retry_scheduled"
	EventJobRunSucceeded        EventType = "job.run.succeeded"
	EventJobRunFailed           EventType = "job.run.failed"
	EventJobRunCanceled         EventType = "job.run.canceled"
	EventJobRunDenied           EventType = "job.run.denied"
)

// Event is a single audit log entry.
type Event struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      EventType `json:"type"`
	ProbeID   string    `json:"probe_id,omitempty"`
	Actor     string    `json:"actor,omitempty"` // who initiated (user, system, probe)
	Summary   string    `json:"summary"`
	Detail    any       `json:"detail,omitempty"`
	Before    any       `json:"before,omitempty"` // state before change
	After     any       `json:"after,omitempty"`  // state after change
}

// Log is an append-only audit log.
type Log struct {
	events []Event
	mu     sync.RWMutex
	maxLen int // ring buffer size (0 = unbounded)
}

// NewLog creates a new audit log. maxLen=0 means unbounded.
func NewLog(maxLen int) *Log {
	return &Log{
		events: make([]Event, 0, 1024),
		maxLen: maxLen,
	}
}

// Record appends an event to the log.
func (l *Log) Record(evt Event) {
	if evt.ID == "" {
		evt.ID = uuid.New().String()
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, evt)

	// Ring buffer: drop oldest if over capacity
	if l.maxLen > 0 && len(l.events) > l.maxLen {
		l.events = l.events[len(l.events)-l.maxLen:]
	}
}

// Emit is a convenience for recording a new event with minimal args.
func (l *Log) Emit(typ EventType, probeID, actor, summary string) {
	l.Record(Event{
		Type:    typ,
		ProbeID: probeID,
		Actor:   actor,
		Summary: summary,
	})
}

// Query returns events matching the filter. limit=0 means all.
type Filter struct {
	ProbeID string
	Type    EventType
	Since   time.Time
	Until   time.Time
	Cursor  string
	Limit   int
}

// Query returns filtered events, newest first.
func (l *Log) Query(f Filter) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var result []Event

	// Walk backwards (newest first)
	for i := len(l.events) - 1; i >= 0; i-- {
		evt := l.events[i]

		if f.ProbeID != "" && evt.ProbeID != f.ProbeID {
			continue
		}
		if f.Type != "" && evt.Type != f.Type {
			continue
		}
		if !f.Since.IsZero() && evt.Timestamp.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && evt.Timestamp.After(f.Until) {
			continue
		}

		result = append(result, evt)

		if f.Limit > 0 && len(result) >= f.Limit {
			break
		}
	}

	return result
}

// Recent returns the N most recent events.
func (l *Log) Recent(n int) []Event {
	return l.Query(Filter{Limit: n})
}

// Count returns total event count.
func (l *Log) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.events)
}

// MarshalJSON exports all events as JSON (for API responses).
func (l *Log) MarshalJSON() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return json.Marshal(l.events)
}

// Login audit event types.
const (
	EventLoginSuccess        EventType = "auth.login"
	EventLoginFailed         EventType = "auth.login_failed"
	EventAuthorizationDenied EventType = "auth.authorization_denied"
)
