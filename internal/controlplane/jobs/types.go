package jobs

import "time"

const (
	TargetKindProbe = "probe"
	TargetKindTag   = "tag"
	TargetKindAll   = "all"

	RunStatusPending  = "pending"
	RunStatusRunning  = "running"
	RunStatusSuccess  = "success"
	RunStatusFailed   = "failed"
	RunStatusCanceled = "canceled"
)

// Job describes a scheduled command execution definition.
type Job struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Command     string       `json:"command"`
	Schedule    string       `json:"schedule"`
	Target      Target       `json:"target"`
	RetryPolicy *RetryPolicy `json:"retry_policy,omitempty"`
	Enabled     bool         `json:"enabled"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	LastRunAt   *time.Time   `json:"last_run_at,omitempty"`
	LastStatus  string       `json:"last_status"`
}

// RetryPolicy configures exponential retry behavior for job runs.
// MaxAttempts includes the first attempt.
type RetryPolicy struct {
	MaxAttempts    int     `json:"max_attempts,omitempty"`
	InitialBackoff string  `json:"initial_backoff,omitempty"`
	Multiplier     float64 `json:"multiplier,omitempty"`
	MaxBackoff     string  `json:"max_backoff,omitempty"`
}

// Target identifies which probes a job should run on.
type Target struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// JobRun captures one execution attempt of a job on a single probe.
type JobRun struct {
	ID               string     `json:"id"`
	JobID            string     `json:"job_id"`
	ProbeID          string     `json:"probe_id"`
	RequestID        string     `json:"request_id"`
	ExecutionID      string     `json:"execution_id,omitempty"`
	Attempt          int        `json:"attempt"`
	MaxAttempts      int        `json:"max_attempts"`
	RetryScheduledAt *time.Time `json:"retry_scheduled_at,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	Status           string     `json:"status"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	Output           string     `json:"output,omitempty"`
}
