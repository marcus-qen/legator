package sandbox

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
	_ "modernc.org/sqlite"
)

// Store provides persistent sandbox session storage backed by SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite-backed sandbox store at dbPath.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sandbox_sessions (
		id            TEXT    PRIMARY KEY,
		workspace_id  TEXT    NOT NULL DEFAULT '',
		probe_id      TEXT    NOT NULL DEFAULT '',
		template_id   TEXT    NOT NULL DEFAULT '',
		runtime_class TEXT    NOT NULL DEFAULT '',
		state         TEXT    NOT NULL DEFAULT 'created',
		task_id       TEXT    NOT NULL DEFAULT '',
		created_by    TEXT    NOT NULL DEFAULT '',
		created_at    TEXT    NOT NULL,
		updated_at    TEXT    NOT NULL,
		destroyed_at  TEXT,
		ttl_ns        INTEGER NOT NULL DEFAULT 0,
		metadata      TEXT    NOT NULL DEFAULT '{}',
		error_message TEXT    NOT NULL DEFAULT ''
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create sandbox_sessions table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sbx_workspace ON sandbox_sessions(workspace_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sbx_state ON sandbox_sessions(state)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sbx_probe ON sandbox_sessions(probe_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sbx_created ON sandbox_sessions(created_at)`)

	if err := migration.EnsureVersion(db, 1); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure schema version: %w", err)
	}

	return &Store{db: db}, nil
}

// Close shuts down the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// Create inserts a new sandbox session. The session ID is generated if empty.
// State is forced to "created".
func (s *Store) Create(sess *SandboxSession) (*SandboxSession, error) {
	if sess.ID == "" {
		sess.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	sess.CreatedAt = now
	sess.UpdatedAt = now
	sess.State = StateCreated

	meta, err := json.Marshal(sess.Metadata)
	if err != nil {
		meta = []byte("{}")
	}

	var destroyedAt *string
	if sess.DestroyedAt != nil {
		s := sess.DestroyedAt.UTC().Format(time.RFC3339Nano)
		destroyedAt = &s
	}

	_, err = s.db.Exec(`INSERT INTO sandbox_sessions
		(id, workspace_id, probe_id, template_id, runtime_class, state, task_id,
		 created_by, created_at, updated_at, destroyed_at, ttl_ns, metadata, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID,
		sess.WorkspaceID,
		sess.ProbeID,
		sess.TemplateID,
		sess.RuntimeClass,
		sess.State,
		sess.TaskID,
		sess.CreatedBy,
		sess.CreatedAt.Format(time.RFC3339Nano),
		sess.UpdatedAt.Format(time.RFC3339Nano),
		destroyedAt,
		int64(sess.TTL),
		string(meta),
		sess.ErrorMessage,
	)
	if err != nil {
		return nil, fmt.Errorf("insert sandbox session: %w", err)
	}

	return sess, nil
}

// Get retrieves a session by ID. Returns (nil, nil) if not found.
func (s *Store) Get(id string) (*SandboxSession, error) {
	row := s.db.QueryRow(`SELECT id, workspace_id, probe_id, template_id, runtime_class, state,
		task_id, created_by, created_at, updated_at, destroyed_at, ttl_ns, metadata, error_message
		FROM sandbox_sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return sess, err
}

// GetForWorkspace retrieves a session by ID, returning nil if it exists but
// belongs to a different workspace (workspace isolation).
func (s *Store) GetForWorkspace(id, workspaceID string) (*SandboxSession, error) {
	sess, err := s.Get(id)
	if err != nil || sess == nil {
		return sess, err
	}
	if workspaceID != "" && sess.WorkspaceID != "" && sess.WorkspaceID != workspaceID {
		return nil, nil
	}
	return sess, nil
}

// List returns sessions matching the filter.
func (s *Store) List(f ListFilter) ([]*SandboxSession, error) {
	query := `SELECT id, workspace_id, probe_id, template_id, runtime_class, state,
		task_id, created_by, created_at, updated_at, destroyed_at, ttl_ns, metadata, error_message
		FROM sandbox_sessions WHERE 1=1`
	var args []any

	if f.WorkspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, f.WorkspaceID)
	}
	if f.State != "" {
		query += " AND state = ?"
		args = append(args, f.State)
	}
	if f.ProbeID != "" {
		query += " AND probe_id = ?"
		args = append(args, f.ProbeID)
	}

	query += " ORDER BY created_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sandbox sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*SandboxSession
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// Update persists changes to metadata, task_id, error_message and updated_at.
// It does NOT change state (use Transition for that).
func (s *Store) Update(sess *SandboxSession) error {
	sess.UpdatedAt = time.Now().UTC()
	meta, err := json.Marshal(sess.Metadata)
	if err != nil {
		meta = []byte("{}")
	}

	res, err := s.db.Exec(`UPDATE sandbox_sessions
		SET task_id=?, metadata=?, error_message=?, updated_at=?
		WHERE id=?`,
		sess.TaskID,
		string(meta),
		sess.ErrorMessage,
		sess.UpdatedAt.Format(time.RFC3339Nano),
		sess.ID,
	)
	if err != nil {
		return fmt.Errorf("update sandbox session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("sandbox session %q not found", sess.ID)
	}
	return nil
}

// Transition atomically moves a session from fromState to toState.
// Returns an error if the transition is invalid or the current state
// doesn't match fromState (optimistic locking).
func (s *Store) Transition(id, fromState, toState string) (*SandboxSession, error) {
	if err := ValidateTransition(fromState, toState); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	var destroyedAtStr *string
	if toState == StateDestroyed {
		s := nowStr
		destroyedAtStr = &s
	}

	var res sql.Result
	var execErr error

	if destroyedAtStr != nil {
		res, execErr = s.db.Exec(`UPDATE sandbox_sessions
			SET state=?, updated_at=?, destroyed_at=?
			WHERE id=? AND state=?`,
			toState, nowStr, *destroyedAtStr, id, fromState,
		)
	} else {
		res, execErr = s.db.Exec(`UPDATE sandbox_sessions
			SET state=?, updated_at=?
			WHERE id=? AND state=?`,
			toState, nowStr, id, fromState,
		)
	}

	if execErr != nil {
		return nil, fmt.Errorf("transition sandbox session: %w", execErr)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		// Either not found or state mismatch
		existing, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("sandbox session %q not found", id)
		}
		return nil, fmt.Errorf("state mismatch: expected %q, current is %q", fromState, existing.State)
	}

	return s.Get(id)
}

// Delete removes a session record. For soft semantics, prefer Transition to
// StateDestroyed; Delete is a hard removal.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM sandbox_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete sandbox session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("sandbox session %q not found", id)
	}
	return nil
}

// Count returns the total number of persisted sessions (optionally filtered
// to a workspace).
func (s *Store) Count(workspaceID string) int {
	q := "SELECT COUNT(*) FROM sandbox_sessions"
	var args []any
	if workspaceID != "" {
		q += " WHERE workspace_id = ?"
		args = append(args, workspaceID)
	}
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0
	}
	return n
}

// ── internal helpers ─────────────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(scanner rowScanner) (*SandboxSession, error) {
	var (
		sess         SandboxSession
		createdAt    string
		updatedAt    string
		destroyedAt  sql.NullString
		ttlNS        int64
		metadataJSON string
	)

	err := scanner.Scan(
		&sess.ID,
		&sess.WorkspaceID,
		&sess.ProbeID,
		&sess.TemplateID,
		&sess.RuntimeClass,
		&sess.State,
		&sess.TaskID,
		&sess.CreatedBy,
		&createdAt,
		&updatedAt,
		&destroyedAt,
		&ttlNS,
		&metadataJSON,
		&sess.ErrorMessage,
	)
	if err != nil {
		return nil, err
	}

	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	sess.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if destroyedAt.Valid && strings.TrimSpace(destroyedAt.String) != "" {
		t, _ := time.Parse(time.RFC3339Nano, destroyedAt.String)
		sess.DestroyedAt = &t
	}
	sess.TTL = time.Duration(ttlNS)

	if metadataJSON != "" && metadataJSON != "null" {
		_ = json.Unmarshal([]byte(metadataJSON), &sess.Metadata)
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]string)
	}

	return &sess, nil
}
