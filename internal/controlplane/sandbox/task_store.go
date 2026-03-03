package sandbox

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TaskStore provides SQLite persistence for sandbox tasks.
// It shares the same *sql.DB as the sandbox Store.
type TaskStore struct {
	db *sql.DB
}

// NewTaskStore creates a TaskStore backed by the given database handle and
// ensures the sandbox_tasks table and indexes exist.
func NewTaskStore(db *sql.DB) (*TaskStore, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sandbox_tasks (
		id            TEXT    PRIMARY KEY,
		sandbox_id    TEXT    NOT NULL,
		workspace_id  TEXT    NOT NULL DEFAULT '',
		kind          TEXT    NOT NULL,
		command       TEXT    NOT NULL DEFAULT '[]',
		repo_url      TEXT    NOT NULL DEFAULT '',
		repo_branch   TEXT    NOT NULL DEFAULT '',
		repo_command  TEXT    NOT NULL DEFAULT '[]',
		image         TEXT    NOT NULL DEFAULT '',
		timeout_secs  INTEGER NOT NULL DEFAULT 300,
		state         TEXT    NOT NULL DEFAULT 'queued',
		exit_code     INTEGER NOT NULL DEFAULT 0,
		output        TEXT    NOT NULL DEFAULT '',
		created_at    TEXT    NOT NULL,
		started_at    TEXT,
		completed_at  TEXT,
		error_message TEXT    NOT NULL DEFAULT ''
	)`); err != nil {
		return nil, fmt.Errorf("create sandbox_tasks table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_sandbox ON sandbox_tasks(sandbox_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_workspace ON sandbox_tasks(workspace_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_state ON sandbox_tasks(state)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_task_created ON sandbox_tasks(created_at)`)

	return &TaskStore{db: db}, nil
}

// CreateTask inserts a new task in queued state. ID is generated if empty.
func (ts *TaskStore) CreateTask(task *Task) (*Task, error) {
	if task.ID == "" {
		task.ID = uuid.New().String()
	}
	task.CreatedAt = time.Now().UTC()
	task.State = TaskStateQueued

	if task.TimeoutSecs <= 0 {
		task.TimeoutSecs = DefaultTaskTimeoutSecs
	}
	if task.TimeoutSecs > MaxTaskTimeoutSecs {
		task.TimeoutSecs = MaxTaskTimeoutSecs
	}

	if task.Command == nil {
		task.Command = []string{}
	}
	if task.RepoCommand == nil {
		task.RepoCommand = []string{}
	}

	cmdJSON, _ := json.Marshal(task.Command)
	repoCommandJSON, _ := json.Marshal(task.RepoCommand)

	// Cap output at MaxOutputBytes just in case.
	output := task.Output
	if len(output) > MaxOutputBytes {
		output = output[:MaxOutputBytes]
	}

	_, err := ts.db.Exec(`INSERT INTO sandbox_tasks
		(id, sandbox_id, workspace_id, kind, command, repo_url, repo_branch,
		 repo_command, image, timeout_secs, state, exit_code, output,
		 created_at, started_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID,
		task.SandboxID,
		task.WorkspaceID,
		task.Kind,
		string(cmdJSON),
		task.RepoURL,
		task.RepoBranch,
		string(repoCommandJSON),
		task.Image,
		task.TimeoutSecs,
		task.State,
		task.ExitCode,
		output,
		task.CreatedAt.Format(time.RFC3339Nano),
		nil, // started_at
		nil, // completed_at
		task.ErrorMessage,
	)
	if err != nil {
		return nil, fmt.Errorf("insert sandbox task: %w", err)
	}
	return task, nil
}

// GetTask retrieves a task by ID. Returns (nil, nil) if not found.
func (ts *TaskStore) GetTask(id string) (*Task, error) {
	row := ts.db.QueryRow(`SELECT id, sandbox_id, workspace_id, kind, command, repo_url,
		repo_branch, repo_command, image, timeout_secs, state, exit_code, output,
		created_at, started_at, completed_at, error_message
		FROM sandbox_tasks WHERE id = ?`, id)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return task, err
}

// GetTaskForWorkspace retrieves a task by ID, enforcing workspace isolation.
func (ts *TaskStore) GetTaskForWorkspace(id, workspaceID string) (*Task, error) {
	task, err := ts.GetTask(id)
	if err != nil || task == nil {
		return task, err
	}
	if workspaceID != "" && task.WorkspaceID != "" && task.WorkspaceID != workspaceID {
		return nil, nil
	}
	return task, nil
}

// ListTasks returns tasks matching the filter, ordered by created_at DESC.
func (ts *TaskStore) ListTasks(f TaskListFilter) ([]*Task, error) {
	query := `SELECT id, sandbox_id, workspace_id, kind, command, repo_url,
		repo_branch, repo_command, image, timeout_secs, state, exit_code, output,
		created_at, started_at, completed_at, error_message
		FROM sandbox_tasks WHERE 1=1`
	var args []any

	if f.SandboxID != "" {
		query += " AND sandbox_id = ?"
		args = append(args, f.SandboxID)
	}
	if f.WorkspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, f.WorkspaceID)
	}
	if f.State != "" {
		query += " AND state = ?"
		args = append(args, f.State)
	}

	query += " ORDER BY created_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := ts.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sandbox tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// UpdateTask persists changes to output, exit_code, error_message, started_at,
// and completed_at. Does NOT change state (use TransitionTask for that).
func (ts *TaskStore) UpdateTask(task *Task) error {
	output := task.Output
	if len(output) > MaxOutputBytes {
		output = output[:MaxOutputBytes]
	}

	var startedAt, completedAt *string
	if task.StartedAt != nil {
		s := task.StartedAt.UTC().Format(time.RFC3339Nano)
		startedAt = &s
	}
	if task.CompletedAt != nil {
		s := task.CompletedAt.UTC().Format(time.RFC3339Nano)
		completedAt = &s
	}

	res, err := ts.db.Exec(`UPDATE sandbox_tasks
		SET exit_code=?, output=?, error_message=?, started_at=?, completed_at=?
		WHERE id=?`,
		task.ExitCode,
		output,
		task.ErrorMessage,
		startedAt,
		completedAt,
		task.ID,
	)
	if err != nil {
		return fmt.Errorf("update sandbox task: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("sandbox task %q not found", task.ID)
	}
	return nil
}

// TransitionTask atomically moves a task from fromState to toState.
// Returns an error if the transition is invalid or the current state doesn't
// match fromState (optimistic locking).
func (ts *TaskStore) TransitionTask(id, fromState, toState string) (*Task, error) {
	if err := ValidateTaskTransition(fromState, toState); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	var res sql.Result
	var execErr error

	switch toState {
	case TaskStateRunning:
		res, execErr = ts.db.Exec(`UPDATE sandbox_tasks
			SET state=?, started_at=?
			WHERE id=? AND state=?`,
			toState, nowStr, id, fromState,
		)
	case TaskStateSucceeded, TaskStateFailed, TaskStateCancelled:
		res, execErr = ts.db.Exec(`UPDATE sandbox_tasks
			SET state=?, completed_at=?
			WHERE id=? AND state=?`,
			toState, nowStr, id, fromState,
		)
	default:
		res, execErr = ts.db.Exec(`UPDATE sandbox_tasks
			SET state=?
			WHERE id=? AND state=?`,
			toState, id, fromState,
		)
	}

	if execErr != nil {
		return nil, fmt.Errorf("transition sandbox task: %w", execErr)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		existing, err := ts.GetTask(id)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("sandbox task %q not found", id)
		}
		return nil, fmt.Errorf("state mismatch: expected %q, current is %q", fromState, existing.State)
	}

	return ts.GetTask(id)
}

// ── internal helpers ─────────────────────────────────────────────────────────

type taskRowScanner interface {
	Scan(dest ...any) error
}

func scanTask(scanner taskRowScanner) (*Task, error) {
	var (
		task            Task
		commandJSON     string
		repoCommandJSON string
		createdAt       string
		startedAt       sql.NullString
		completedAt     sql.NullString
	)

	err := scanner.Scan(
		&task.ID,
		&task.SandboxID,
		&task.WorkspaceID,
		&task.Kind,
		&commandJSON,
		&task.RepoURL,
		&task.RepoBranch,
		&repoCommandJSON,
		&task.Image,
		&task.TimeoutSecs,
		&task.State,
		&task.ExitCode,
		&task.Output,
		&createdAt,
		&startedAt,
		&completedAt,
		&task.ErrorMessage,
	)
	if err != nil {
		return nil, err
	}

	task.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)

	if startedAt.Valid && strings.TrimSpace(startedAt.String) != "" {
		t, _ := time.Parse(time.RFC3339Nano, startedAt.String)
		task.StartedAt = &t
	}
	if completedAt.Valid && strings.TrimSpace(completedAt.String) != "" {
		t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
		task.CompletedAt = &t
	}

	if commandJSON != "" && commandJSON != "null" {
		_ = json.Unmarshal([]byte(commandJSON), &task.Command)
	}
	if task.Command == nil {
		task.Command = []string{}
	}

	if repoCommandJSON != "" && repoCommandJSON != "null" {
		_ = json.Unmarshal([]byte(repoCommandJSON), &task.RepoCommand)
	}
	if task.RepoCommand == nil {
		task.RepoCommand = []string{}
	}

	return &task, nil
}
