package sandbox

import (
	"fmt"
	"time"
)

// Task state constants.
const (
	TaskStateQueued    = "queued"
	TaskStateRunning   = "running"
	TaskStateSucceeded = "succeeded"
	TaskStateFailed    = "failed"
	TaskStateCancelled = "cancelled"
)

// Task kind constants.
const (
	TaskKindCommand = "command"
	TaskKindRepo    = "repo"
)

// MaxOutputBytes caps the task output field at 64 KB.
const MaxOutputBytes = 64 * 1024

// DefaultTaskTimeoutSecs is the default task timeout in seconds.
const DefaultTaskTimeoutSecs = 300

// MaxTaskTimeoutSecs is the maximum permitted task timeout.
const MaxTaskTimeoutSecs = 3600

// validTaskTransitions defines the allowed task state machine moves.
var validTaskTransitions = map[string]map[string]bool{
	TaskStateQueued: {
		TaskStateRunning:   true,
		TaskStateCancelled: true,
	},
	TaskStateRunning: {
		TaskStateSucceeded: true,
		TaskStateFailed:    true,
		TaskStateCancelled: true,
	},
	// Terminal states: no outgoing transitions.
	TaskStateSucceeded: {},
	TaskStateFailed:    {},
	TaskStateCancelled: {},
}

// ValidateTaskTransition returns an error if transitioning from fromState to
// toState is not permitted by the task state machine.
func ValidateTaskTransition(fromState, toState string) error {
	allowed, ok := validTaskTransitions[fromState]
	if !ok {
		return fmt.Errorf("unknown task state %q", fromState)
	}
	if !allowed[toState] {
		return fmt.Errorf("task transition %q → %q is not allowed", fromState, toState)
	}
	return nil
}

// Task is the domain model for a task execution within a sandbox session.
type Task struct {
	ID           string     `json:"id"`
	SandboxID    string     `json:"sandbox_id"`
	WorkspaceID  string     `json:"workspace_id"`
	Kind         string     `json:"kind"` // "command" or "repo"
	Command      []string   `json:"command,omitempty"`
	RepoURL      string     `json:"repo_url,omitempty"`
	RepoBranch   string     `json:"repo_branch,omitempty"`
	RepoCommand  []string   `json:"repo_command,omitempty"`
	Image        string     `json:"image,omitempty"`
	TimeoutSecs  int        `json:"timeout_secs"`
	State        string     `json:"state"`
	ExitCode     int        `json:"exit_code,omitempty"`
	Output       string     `json:"output,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
}

// IsTerminal reports whether the task has reached a terminal state.
func (t *Task) IsTerminal() bool {
	return t.State == TaskStateSucceeded || t.State == TaskStateFailed || t.State == TaskStateCancelled
}

// TaskListFilter controls what tasks are returned by TaskStore.ListTasks.
type TaskListFilter struct {
	SandboxID   string
	WorkspaceID string
	State       string
	Limit       int
}
