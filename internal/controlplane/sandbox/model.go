// Package sandbox implements the sandbox session model, state machine,
// SQLite persistence, REST API handlers, and event bus integration for
// the Legator control plane.
package sandbox

import (
	"fmt"
	"time"
)

// State constants for a sandbox session lifecycle.
const (
	StateCreated      = "created"
	StateProvisioning = "provisioning"
	StateReady        = "ready"
	StateRunning      = "running"
	StateFailed       = "failed"
	StateDestroyed    = "destroyed"
)

// validTransitions defines the allowed state machine moves.
// Map: fromState → set of valid toStates.
var validTransitions = map[string]map[string]bool{
	StateCreated: {
		StateProvisioning: true,
	},
	StateProvisioning: {
		StateReady:  true,
		StateFailed: true,
	},
	StateReady: {
		StateRunning:   true,
		StateDestroyed: true,
	},
	StateRunning: {
		StateFailed:    true,
		StateDestroyed: true,
	},
	// Terminal states: no outgoing transitions.
	StateFailed:    {},
	StateDestroyed: {},
}

// ValidateTransition returns an error if transitioning from fromState to
// toState is not permitted by the state machine.
func ValidateTransition(fromState, toState string) error {
	allowed, ok := validTransitions[fromState]
	if !ok {
		return fmt.Errorf("unknown state %q", fromState)
	}
	if !allowed[toState] {
		return fmt.Errorf("transition %q → %q is not allowed", fromState, toState)
	}
	return nil
}

// SandboxSession is the core domain model.
type SandboxSession struct {
	ID           string            `json:"id"`
	WorkspaceID  string            `json:"workspace_id"`
	ProbeID      string            `json:"probe_id"`
	TemplateID   string            `json:"template_id,omitempty"`
	RuntimeClass string            `json:"runtime_class"`
	State        string            `json:"state"`
	TaskID       string            `json:"task_id,omitempty"`
	CreatedBy    string            `json:"created_by"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	DestroyedAt  *time.Time        `json:"destroyed_at,omitempty"`
	TTL          time.Duration     `json:"ttl_ns,omitempty"` // stored as nanoseconds
	Metadata     map[string]string `json:"metadata,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
}

// IsTerminal reports whether the session has reached a terminal state
// (failed or destroyed).
func (s *SandboxSession) IsTerminal() bool {
	return s.State == StateFailed || s.State == StateDestroyed
}

// ListFilter controls what sessions are returned by Store.List.
type ListFilter struct {
	WorkspaceID string
	State       string
	ProbeID     string
	Limit       int
}
