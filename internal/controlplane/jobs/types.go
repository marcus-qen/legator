package jobs

import "time"

const (
	TargetKindProbe = "probe"
	TargetKindTag   = "tag"
	TargetKindAll   = "all"

	RunStatusRunning = "running"
	RunStatusSuccess = "success"
	RunStatusFailed  = "failed"
)

// Job describes a scheduled command execution definition.
type Job struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Command    string     `json:"command"`
	Schedule   string     `json:"schedule"`
	Target     Target     `json:"target"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastStatus string     `json:"last_status"`
}

// Target identifies which probes a job should run on.
type Target struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// JobRun captures one execution attempt of a job on a single probe.
type JobRun struct {
	ID        string     `json:"id"`
	JobID     string     `json:"job_id"`
	ProbeID   string     `json:"probe_id"`
	RequestID string     `json:"request_id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Status    string     `json:"status"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	Output    string     `json:"output,omitempty"`
}
