package sandbox

import "time"

// MaxChunkSize is the maximum allowed size (in bytes) for a single output
// chunk's data field. Chunks exceeding this limit are rejected at ingest.
const MaxChunkSize = 8 * 1024 // 8 KiB

// StreamType constants for the stream field.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// OutputChunk is a single unit of captured output from a task running inside
// a sandbox. Chunks are append-only and ordered by Sequence within a task.
type OutputChunk struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	SandboxID string    `json:"sandbox_id"`
	Sequence  int64     `json:"sequence"` // monotonically increasing per task
	Stream    string    `json:"stream"`   // "stdout" or "stderr"
	Data      string    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}
