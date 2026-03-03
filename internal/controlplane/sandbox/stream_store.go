package sandbox

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// StreamStore provides SQLite persistence for sandbox output chunks.
// It shares the same *sql.DB as the sandbox Store and TaskStore.
type StreamStore struct {
	db *sql.DB
}

// NewStreamStore creates a StreamStore backed by the given database handle
// and ensures the sandbox_output_chunks table and indexes exist.
func NewStreamStore(db *sql.DB) (*StreamStore, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sandbox_output_chunks (
		id          TEXT    PRIMARY KEY,
		task_id     TEXT    NOT NULL DEFAULT '',
		sandbox_id  TEXT    NOT NULL,
		sequence    INTEGER NOT NULL,
		stream      TEXT    NOT NULL DEFAULT 'stdout',
		data        TEXT    NOT NULL DEFAULT '',
		timestamp   TEXT    NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("create sandbox_output_chunks table: %w", err)
	}

	// Primary query pattern: ordered replay for a task.
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_chunk_task_seq
		ON sandbox_output_chunks(task_id, sequence)`)
	// Secondary pattern: all chunks for a sandbox (cross-task view).
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_chunk_sandbox_seq
		ON sandbox_output_chunks(sandbox_id, sequence)`)

	return &StreamStore{db: db}, nil
}

// AppendChunk persists a single output chunk. If the chunk has no ID one is
// generated. Timestamp is set to now if zero.
func (ss *StreamStore) AppendChunk(chunk *OutputChunk) error {
	if chunk.ID == "" {
		chunk.ID = uuid.New().String()
	}
	if chunk.Timestamp.IsZero() {
		chunk.Timestamp = time.Now().UTC()
	}

	_, err := ss.db.Exec(`INSERT INTO sandbox_output_chunks
		(id, task_id, sandbox_id, sequence, stream, data, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		chunk.ID,
		chunk.TaskID,
		chunk.SandboxID,
		chunk.Sequence,
		chunk.Stream,
		chunk.Data,
		chunk.Timestamp.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert output chunk: %w", err)
	}
	return nil
}

// AppendChunks persists multiple chunks in a single transaction.
func (ss *StreamStore) AppendChunks(chunks []*OutputChunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := ss.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare(`INSERT INTO sandbox_output_chunks
		(id, task_id, sandbox_id, sequence, stream, data, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, chunk := range chunks {
		if chunk.ID == "" {
			chunk.ID = uuid.New().String()
		}
		ts := chunk.Timestamp
		if ts.IsZero() {
			ts = now
			chunk.Timestamp = ts
		}
		if _, err = stmt.Exec(
			chunk.ID,
			chunk.TaskID,
			chunk.SandboxID,
			chunk.Sequence,
			chunk.Stream,
			chunk.Data,
			ts.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert chunk %d: %w", chunk.Sequence, err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ListChunks returns chunks for a specific task, ordered by sequence,
// with sequence > sinceSequence. Pass sinceSequence=0 to get all.
// Limit is capped to 1000; default 100.
func (ss *StreamStore) ListChunks(taskID string, sinceSequence int64, limit int) ([]*OutputChunk, error) {
	limit = clampLimit(limit)
	rows, err := ss.db.Query(`SELECT id, task_id, sandbox_id, sequence, stream, data, timestamp
		FROM sandbox_output_chunks
		WHERE task_id = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?`, taskID, sinceSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list chunks by task: %w", err)
	}
	defer rows.Close()
	return scanChunks(rows)
}

// ListChunksBySandbox returns all chunks for a sandbox (across all tasks),
// ordered by sequence, with sequence > sinceSequence.
func (ss *StreamStore) ListChunksBySandbox(sandboxID string, sinceSequence int64, limit int) ([]*OutputChunk, error) {
	limit = clampLimit(limit)
	rows, err := ss.db.Query(`SELECT id, task_id, sandbox_id, sequence, stream, data, timestamp
		FROM sandbox_output_chunks
		WHERE sandbox_id = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?`, sandboxID, sinceSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list chunks by sandbox: %w", err)
	}
	defer rows.Close()
	return scanChunks(rows)
}

// PurgeChunks deletes all output chunks for a sandbox (called on destroy).
func (ss *StreamStore) PurgeChunks(sandboxID string) error {
	_, err := ss.db.Exec(`DELETE FROM sandbox_output_chunks WHERE sandbox_id = ?`, sandboxID)
	if err != nil {
		return fmt.Errorf("purge chunks for sandbox %q: %w", sandboxID, err)
	}
	return nil
}

// NextSequence returns the next available sequence number for a task.
// Returns 1 if no chunks exist yet.
func (ss *StreamStore) NextSequence(taskID string) (int64, error) {
	var max sql.NullInt64
	err := ss.db.QueryRow(`SELECT MAX(sequence) FROM sandbox_output_chunks WHERE task_id = ?`, taskID).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("query max sequence: %w", err)
	}
	if !max.Valid {
		return 1, nil
	}
	return max.Int64 + 1, nil
}

// ── internal helpers ─────────────────────────────────────────────────────────

func clampLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

func scanChunks(rows *sql.Rows) ([]*OutputChunk, error) {
	var chunks []*OutputChunk
	for rows.Next() {
		var (
			c     OutputChunk
			tsStr string
		)
		if err := rows.Scan(
			&c.ID,
			&c.TaskID,
			&c.SandboxID,
			&c.Sequence,
			&c.Stream,
			&c.Data,
			&tsStr,
		); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		c.Timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
		chunks = append(chunks, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return chunks, nil
}
