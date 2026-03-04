package sandbox

import (
	"time"
)

// ReplayEventKind constants for the kind field.
const (
	ReplayEventKindOutput       = "output"
	ReplayEventKindTaskState    = "task_state"
	ReplayEventKindArtifact     = "artifact"
	ReplayEventKindSandboxState = "sandbox_state"
)

// TaskStateSummary is a lightweight summary of a task state change event.
type TaskStateSummary struct {
	TaskID    string `json:"task_id"`
	Kind      string `json:"kind"`
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state"`
}

// ArtifactSummary is a lightweight summary of an artifact event.
type ArtifactSummary struct {
	ArtifactID  string `json:"artifact_id"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	Size        int64  `json:"size"`
	DiffSummary string `json:"diff_summary,omitempty"`
}

// SandboxStateSummary is a lightweight summary of a sandbox state change.
type SandboxStateSummary struct {
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state"`
}

// ReplayEvent is a single entry in a replay timeline.
type ReplayEvent struct {
	Timestamp time.Time   `json:"timestamp"`
	Kind      string      `json:"kind"` // "output", "task_state", "artifact", "sandbox_state"
	Data      interface{} `json:"data"` // OutputChunk, TaskStateSummary, ArtifactSummary, or SandboxStateSummary
}

// ReplayTimeline is the complete replay data for a sandbox session.
type ReplayTimeline struct {
	SandboxID  string        `json:"sandbox_id"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    time.Time     `json:"end_time"`
	Duration   time.Duration `json:"duration_ns"`
	EventCount int           `json:"event_count"`
	Events     []ReplayEvent `json:"events"`
}

// ReplaySummary is a lightweight version of ReplayTimeline without the events array.
type ReplaySummary struct {
	SandboxID  string        `json:"sandbox_id"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    time.Time     `json:"end_time"`
	Duration   time.Duration `json:"duration_ns"`
	EventCount int           `json:"event_count"`
}

// Summary returns the lightweight metadata summary for this timeline.
func (rt *ReplayTimeline) Summary() ReplaySummary {
	return ReplaySummary{
		SandboxID:  rt.SandboxID,
		StartTime:  rt.StartTime,
		EndTime:    rt.EndTime,
		Duration:   rt.Duration,
		EventCount: rt.EventCount,
	}
}
