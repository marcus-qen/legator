// Package cmdtracker tracks in-flight commands and routes results back to callers.
// When the HTTP API dispatches a command to a probe, the caller can wait for the result
// via a channel. When the probe sends back a CommandResult over WebSocket, the tracker
// completes the pending request.
package cmdtracker

import (
	"fmt"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// PendingCommand represents a command waiting for a result.
type PendingCommand struct {
	RequestID string
	ProbeID   string
	Command   string
	Level     protocol.CapabilityLevel
	Submitted time.Time
	Result    chan *protocol.CommandResultPayload
}

// Tracker manages in-flight commands.
type Tracker struct {
	pending map[string]*PendingCommand // keyed by request_id
	mu      sync.Mutex
	ttl     time.Duration // auto-expire after this
}

// New creates a Tracker with a TTL for auto-expiry.
func New(ttl time.Duration) *Tracker {
	t := &Tracker{
		pending: make(map[string]*PendingCommand),
		ttl:     ttl,
	}
	go t.reaper()
	return t
}

// Track registers a command as in-flight. Returns the PendingCommand whose
// Result channel will receive the probe's response.
func (t *Tracker) Track(requestID, probeID, command string, level protocol.CapabilityLevel) *PendingCommand {
	pc := &PendingCommand{
		RequestID: requestID,
		ProbeID:   probeID,
		Command:   command,
		Level:     level,
		Submitted: time.Now().UTC(),
		Result:    make(chan *protocol.CommandResultPayload, 1),
	}

	t.mu.Lock()
	t.pending[requestID] = pc
	t.mu.Unlock()

	return pc
}

// Complete delivers a result to the waiting caller. Returns an error if
// the request ID isn't tracked (already expired or unknown).
func (t *Tracker) Complete(requestID string, result *protocol.CommandResultPayload) error {
	t.mu.Lock()
	pc, ok := t.pending[requestID]
	if ok {
		delete(t.pending, requestID)
	}
	t.mu.Unlock()

	if !ok {
		return fmt.Errorf("no pending command for request %s", requestID)
	}

	// Non-blocking send (buffer=1)
	pc.Result <- result
	return nil
}

// Cancel removes a tracked command without delivering a result.
func (t *Tracker) Cancel(requestID string) {
	t.mu.Lock()
	pc, ok := t.pending[requestID]
	if ok {
		delete(t.pending, requestID)
		close(pc.Result)
	}
	t.mu.Unlock()
}

// InFlight returns the number of currently tracked commands.
func (t *Tracker) InFlight() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

// ListPending returns summaries of all pending commands.
func (t *Tracker) ListPending() []PendingSummary {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make([]PendingSummary, 0, len(t.pending))
	now := time.Now().UTC()
	for _, pc := range t.pending {
		result = append(result, PendingSummary{
			RequestID: pc.RequestID,
			ProbeID:   pc.ProbeID,
			Command:   pc.Command,
			Level:     pc.Level,
			Waiting:   now.Sub(pc.Submitted),
		})
	}
	return result
}

// PendingSummary is a JSON-safe view of a pending command.
type PendingSummary struct {
	RequestID string                   `json:"request_id"`
	ProbeID   string                   `json:"probe_id"`
	Command   string                   `json:"command"`
	Level     protocol.CapabilityLevel `json:"level"`
	Waiting   time.Duration            `json:"waiting_ms"`
}

// expire checks for stale pending commands and times them out.
func (t *Tracker) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().UTC().Add(-t.ttl)
	for id, pc := range t.pending {
		if pc.Submitted.Before(cutoff) {
			pc.Result <- &protocol.CommandResultPayload{
				RequestID: pc.RequestID,
				ExitCode:  -1,
				Stderr:    "command timed out waiting for probe response",
				Duration:  int64(t.ttl / time.Millisecond),
			}
			delete(t.pending, id)
		}
	}
}

// reaper runs in a goroutine and periodically calls expire.
func (t *Tracker) reaper() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		t.expire()
	}
}
