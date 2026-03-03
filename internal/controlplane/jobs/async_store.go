package jobs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
)

const (
	defaultAsyncJobListLimit = 50
	maxAsyncJobListLimit     = 500
)

func migrateAsyncJobs(db *sql.DB) error {
runner := migration.NewRunner("jobs", []migration.Migration{
{
Version:     2,
Description: "add async job state machine tables",
Up: func(tx *sql.Tx) error {
if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS async_jobs (
id            TEXT PRIMARY KEY,
probe_id      TEXT NOT NULL,
workspace_id  TEXT NOT NULL DEFAULT '',
request_id    TEXT NOT NULL UNIQUE,
command       TEXT NOT NULL,
args_json     TEXT NOT NULL DEFAULT '[]',
level         TEXT NOT NULL DEFAULT '',
state         TEXT NOT NULL,
status_reason TEXT NOT NULL DEFAULT '',
approval_id   TEXT NOT NULL DEFAULT '',
exit_code     INTEGER,
output        TEXT NOT NULL DEFAULT '',
created_at    TEXT NOT NULL,
updated_at    TEXT NOT NULL,
started_at    TEXT,
finished_at   TEXT,
expires_at    TEXT
)`); err != nil {
return err
}
if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_async_jobs_state_updated ON async_jobs(state, updated_at DESC)`); err != nil {
return err
}
if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_async_jobs_created_at ON async_jobs(created_at DESC)`); err != nil {
return err
}
if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_async_jobs_workspace_id ON async_jobs(workspace_id)`); err != nil {
return err
}
if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_async_jobs_expires_at ON async_jobs(expires_at)`); err != nil {
return err
}
return nil
},
},
{
Version:     3,
Description: "add async queue scheduling index",
Up: func(tx *sql.Tx) error {
_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_async_jobs_state_created ON async_jobs(state, created_at ASC)`)
return err
},
},
{
Version:     4,
Description: "add approval audit columns (approved_by, rejected_by, rejection_reason, approval_deadline)",
Up: func(tx *sql.Tx) error {
for _, col := range []string{
`ALTER TABLE async_jobs ADD COLUMN approved_by TEXT NOT NULL DEFAULT ''`,
`ALTER TABLE async_jobs ADD COLUMN rejected_by TEXT NOT NULL DEFAULT ''`,
`ALTER TABLE async_jobs ADD COLUMN rejection_reason TEXT NOT NULL DEFAULT ''`,
`ALTER TABLE async_jobs ADD COLUMN approval_deadline TEXT`,
} {
if _, err := tx.Exec(col); err != nil {
return err
}
}
return nil
},
},
{
Version:     5,
Description: "add workspace id column to async jobs",
Up: func(tx *sql.Tx) error {
if _, err := tx.Exec(`ALTER TABLE async_jobs ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`); err != nil {
// ignore duplicate column (column may exist in fresh schema)
if !strings.Contains(err.Error(), "duplicate column name") {
	return err
}
}
if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_async_jobs_workspace_id ON async_jobs(workspace_id)`); err != nil {
return err
}
return nil
},
},
})
return runner.Migrate(db)
}

func (s *Store) CreateAsyncJob(job AsyncJob) (*AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	job.ProbeID = strings.TrimSpace(job.ProbeID)
	job.Command = strings.TrimSpace(job.Command)
	job.RequestID = strings.TrimSpace(job.RequestID)
	job.Level = strings.TrimSpace(job.Level)
	job.WorkspaceID = strings.ToLower(strings.TrimSpace(job.WorkspaceID))
	if job.ProbeID == "" {
		return nil, fmt.Errorf("probe_id required")
	}
	if job.Command == "" {
		return nil, fmt.Errorf("command required")
	}
	if job.RequestID == "" {
		job.RequestID = "job-" + uuid.NewString()
	}
	if strings.TrimSpace(job.ID) == "" {
		job.ID = uuid.NewString()
	}
	job.State = normalizeAsyncJobState(job.State)
	if job.State == "" {
		job.State = AsyncJobStateQueued
	}
	if !isKnownAsyncJobState(job.State) {
		return nil, fmt.Errorf("invalid async job state: %s", job.State)
	}

	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	job.StatusReason = strings.TrimSpace(job.StatusReason)

	argsJSON, err := json.Marshal(job.Args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}

	_, err = s.db.Exec(`INSERT INTO async_jobs (
		id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
		exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
		approved_by, rejected_by, rejection_reason, approval_deadline
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		strings.TrimSpace(job.WorkspaceID),
		job.ProbeID,
		job.WorkspaceID,
		job.RequestID,
		job.Command,
		string(argsJSON),
		job.Level,
		string(job.State),
		job.StatusReason,
		strings.TrimSpace(job.ApprovalID),
		nullableInt(job.ExitCode),
		truncateOutput(job.Output),
		job.CreatedAt.UTC().Format(time.RFC3339Nano),
		job.UpdatedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(job.StartedAt),
		nullableTime(job.FinishedAt),
		nullableTime(job.ExpiresAt),
		strings.TrimSpace(job.ApprovedBy),
		strings.TrimSpace(job.RejectedBy),
		strings.TrimSpace(job.RejectionReason),
		nullableTime(job.ApprovalDeadline),
	)
	if err != nil {
		return nil, fmt.Errorf("insert async job: %w", err)
	}
	return s.GetAsyncJob(job.ID)
}

func (s *Store) ListAsyncJobs(limit int) ([]AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	limit = normalizeAsyncJobLimit(limit)
	rows, err := s.db.Query(`SELECT
		id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
		exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
		approved_by, rejected_by, rejection_reason, approval_deadline
		FROM async_jobs
		ORDER BY created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AsyncJob, 0, limit)
	for rows.Next() {
		job, err := scanAsyncJob(rows)
		if err != nil {
			continue
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

func (s *Store) ListAsyncJobsByWorkspace(workspaceID string, limit int) ([]AsyncJob, error) {
if s == nil || s.db == nil {
return nil, fmt.Errorf("store unavailable")
}
workspaceID = strings.ToLower(strings.TrimSpace(workspaceID))
if workspaceID == "" {
return nil, fmt.Errorf("workspace_id required")
}
limit = normalizeAsyncJobLimit(limit)
rows, err := s.db.Query(`SELECT
id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
approved_by, rejected_by, rejection_reason, approval_deadline
FROM async_jobs
WHERE workspace_id = ?
ORDER BY created_at DESC
LIMIT ?`, workspaceID, limit)
if err != nil {
return nil, err
}
defer rows.Close()

out := make([]AsyncJob, 0, limit)
for rows.Next() {
job, err := scanAsyncJob(rows)
if err != nil {
continue
}
out = append(out, *job)
}
return out, rows.Err()
}

func (s *Store) ListAsyncJobsByState(state AsyncJobState, limit int) ([]AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	state = normalizeAsyncJobState(state)
	if !isKnownAsyncJobState(state) {
		return nil, fmt.Errorf("invalid async job state: %s", state)
	}
	limit = normalizeAsyncJobLimit(limit)
	rows, err := s.db.Query(`SELECT
		id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
		exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
		approved_by, rejected_by, rejection_reason, approval_deadline
		FROM async_jobs
		WHERE state = ?
		ORDER BY created_at ASC
		LIMIT ?`, string(state), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AsyncJob, 0, limit)
	for rows.Next() {
		job, err := scanAsyncJob(rows)
		if err != nil {
			continue
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

func (s *Store) CountAsyncJobsByState(states ...AsyncJobState) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("store unavailable")
	}
	normalized := make([]string, 0, len(states))
	for _, state := range states {
		state = normalizeAsyncJobState(state)
		if !isKnownAsyncJobState(state) {
			continue
		}
		normalized = append(normalized, string(state))
	}
	if len(normalized) == 0 {
		return 0, nil
	}

	placeholders := strings.Repeat("?,", len(normalized))
	placeholders = strings.TrimSuffix(placeholders, ",")
	query := `SELECT COUNT(*) FROM async_jobs WHERE state IN (` + placeholders + `)`
	args := make([]any, 0, len(normalized))
	for _, state := range normalized {
		args = append(args, state)
	}

	var count int
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) AsyncJobStateCounts() (map[AsyncJobState]int, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	rows, err := s.db.Query(`SELECT state, COUNT(*) FROM async_jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[AsyncJobState]int{}
	for rows.Next() {
		var (
			state string
			count int
		)
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		normalized := normalizeAsyncJobState(AsyncJobState(state))
		if isKnownAsyncJobState(normalized) {
			counts[normalized] = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func (s *Store) RunningAsyncJobsByProbe() (map[string]int, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	rows, err := s.db.Query(`SELECT probe_id, COUNT(*) FROM async_jobs WHERE state = ? GROUP BY probe_id`, string(AsyncJobStateRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var (
			probeID string
			count   int
		)
		if err := rows.Scan(&probeID, &count); err != nil {
			return nil, err
		}
		counts[strings.TrimSpace(probeID)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func (s *Store) GetAsyncJob(id string) (*AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("job id required")
	}
	row := s.db.QueryRow(`SELECT
		id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
		exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
		approved_by, rejected_by, rejection_reason, approval_deadline
		FROM async_jobs WHERE id = ?`, id)
	return scanAsyncJob(row)
}

func (s *Store) GetAsyncJobInWorkspace(id, workspaceID string) (*AsyncJob, error) {
if s == nil || s.db == nil {
return nil, fmt.Errorf("store unavailable")
}
id = strings.TrimSpace(id)
if id == "" {
return nil, fmt.Errorf("job id required")
}
workspaceID = strings.ToLower(strings.TrimSpace(workspaceID))
if workspaceID == "" {
return nil, fmt.Errorf("workspace_id required")
}
row := s.db.QueryRow(`SELECT
id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
approved_by, rejected_by, rejection_reason, approval_deadline
FROM async_jobs WHERE id = ? AND workspace_id = ?`, id, workspaceID)
return scanAsyncJob(row)
}

func (s *Store) GetAsyncJobByRequestID(requestID string) (*AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("request_id required")
	}
	row := s.db.QueryRow(`SELECT
		id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
		exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
		approved_by, rejected_by, rejection_reason, approval_deadline
		FROM async_jobs WHERE request_id = ?`, requestID)
	return scanAsyncJob(row)
}

func (s *Store) TransitionAsyncJob(id string, toState AsyncJobState, opts AsyncJobTransitionOptions) (*AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("job id required")
	}
	toState = normalizeAsyncJobState(toState)
	if !isKnownAsyncJobState(toState) {
		return nil, fmt.Errorf("invalid async job state: %s", toState)
	}

	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var currentState string
	if err := tx.QueryRow(`SELECT state FROM async_jobs WHERE id = ?`, id).Scan(&currentState); err != nil {
		return nil, err
	}
	fromState := normalizeAsyncJobState(AsyncJobState(currentState))
	if fromState == toState {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.GetAsyncJob(id)
	}
	if !canTransitionAsyncJob(fromState, toState) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidAsyncJobTransition, fromState, toState)
	}

	setClauses := []string{"state = ?", "updated_at = ?"}
	args := []any{string(toState), now.Format(time.RFC3339Nano)}

	if reason := strings.TrimSpace(opts.StatusReason); reason != "" {
		setClauses = append(setClauses, "status_reason = ?")
		args = append(args, reason)
	}
	if approvalID := strings.TrimSpace(opts.ApprovalID); approvalID != "" {
		setClauses = append(setClauses, "approval_id = ?")
		args = append(args, approvalID)
	}
	if opts.ExitCode != nil {
		setClauses = append(setClauses, "exit_code = ?")
		args = append(args, *opts.ExitCode)
	}
	if output := strings.TrimSpace(opts.Output); output != "" {
		setClauses = append(setClauses, "output = ?")
		args = append(args, truncateOutput(output))
	}

	startedAt := opts.StartedAt
	if startedAt == nil && toState == AsyncJobStateRunning {
		ts := now
		startedAt = &ts
	}
	if startedAt != nil {
		setClauses = append(setClauses, "started_at = ?")
		args = append(args, startedAt.UTC().Format(time.RFC3339Nano))
	}

	finishedAt := opts.FinishedAt
	if finishedAt == nil && toState.IsTerminal() {
		ts := now
		finishedAt = &ts
	}
	if finishedAt != nil {
		setClauses = append(setClauses, "finished_at = ?")
		args = append(args, finishedAt.UTC().Format(time.RFC3339Nano))
	}

	if opts.ExpiresAt != nil {
		setClauses = append(setClauses, "expires_at = ?")
		args = append(args, opts.ExpiresAt.UTC().Format(time.RFC3339Nano))
	} else if toState != AsyncJobStateWaitingApproval {
		setClauses = append(setClauses, "expires_at = NULL")
	}
	if approvedBy := strings.TrimSpace(opts.ApprovedBy); approvedBy != "" {
		setClauses = append(setClauses, "approved_by = ?")
		args = append(args, approvedBy)
	}
	if rejectedBy := strings.TrimSpace(opts.RejectedBy); rejectedBy != "" {
		setClauses = append(setClauses, "rejected_by = ?")
		args = append(args, rejectedBy)
	}
	if rejReason := strings.TrimSpace(opts.RejectionReason); rejReason != "" {
		setClauses = append(setClauses, "rejection_reason = ?")
		args = append(args, rejReason)
	}
	if opts.ApprovalDeadline != nil {
		setClauses = append(setClauses, "approval_deadline = ?")
		args = append(args, opts.ApprovalDeadline.UTC().Format(time.RFC3339Nano))
	}

	stmt := `UPDATE async_jobs SET ` + strings.Join(setClauses, ", ") + ` WHERE id = ? AND state = ?`
	args = append(args, id, string(fromState))
	res, err := tx.Exec(stmt, args...)
	if err != nil {
		return nil, err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		var latest string
		if err := tx.QueryRow(`SELECT state FROM async_jobs WHERE id = ?`, id).Scan(&latest); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidAsyncJobTransition, latest, toState)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetAsyncJob(id)
}

func (s *Store) CancelAsyncJob(id, reason string) (*AsyncJob, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "cancelled by request"
	}
	return s.TransitionAsyncJob(id, AsyncJobStateCancelled, AsyncJobTransitionOptions{StatusReason: reason})
}

func (s *Store) ExpireRunningAsyncJobs(reason string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("store unavailable")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "expired while control plane was unavailable"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(`UPDATE async_jobs
		SET state = ?, status_reason = ?, updated_at = ?, finished_at = ?
		WHERE state = ?`,
		string(AsyncJobStateExpired), reason, now, now, string(AsyncJobStateRunning))
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

func (s *Store) ExpireWaitingApprovalAsyncJobs(now time.Time, reason string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("store unavailable")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "approval window expired"
	}
	now = now.UTC()
	res, err := s.db.Exec(`UPDATE async_jobs
		SET state = ?, status_reason = ?, updated_at = ?, finished_at = ?
		WHERE state = ? AND expires_at IS NOT NULL AND expires_at != '' AND expires_at <= ?`,
		string(AsyncJobStateExpired), reason, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), string(AsyncJobStateWaitingApproval), now.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

func normalizeAsyncJobLimit(limit int) int {
	if limit <= 0 {
		return defaultAsyncJobListLimit
	}
	if limit > maxAsyncJobListLimit {
		return maxAsyncJobListLimit
	}
	return limit
}

func scanAsyncJob(s scanner) (*AsyncJob, error) {
	var (
		job                  AsyncJob
		argsJSON             string
		state                string
		createdAt, updatedAt string
		startedAt            sql.NullString
		finishedAt           sql.NullString
		expiresAt            sql.NullString
		exitCode             sql.NullInt64
		approvedBy           string
		rejectedBy           string
		rejectionReason      string
		approvalDeadline     sql.NullString
	)

	if err := s.Scan(
		&job.ID,
		&job.WorkspaceID,
		&job.ProbeID,
		&job.WorkspaceID,
		&job.RequestID,
		&job.Command,
		&argsJSON,
		&job.Level,
		&state,
		&job.StatusReason,
		&job.ApprovalID,
		&exitCode,
		&job.Output,
		&createdAt,
		&updatedAt,
		&startedAt,
		&finishedAt,
		&expiresAt,
		&approvedBy,
		&rejectedBy,
		&rejectionReason,
		&approvalDeadline,
	); err != nil {
		return nil, err
	}

	job.State = normalizeAsyncJobState(AsyncJobState(state))
	job.WorkspaceID = strings.ToLower(strings.TrimSpace(job.WorkspaceID))
	if len(strings.TrimSpace(argsJSON)) > 0 {
		_ = json.Unmarshal([]byte(argsJSON), &job.Args)
	}
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if startedAt.Valid && startedAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, startedAt.String)
		if err == nil {
			job.StartedAt = &ts
		}
	}
	if finishedAt.Valid && finishedAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err == nil {
			job.FinishedAt = &ts
		}
	}
	if expiresAt.Valid && expiresAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, expiresAt.String)
		if err == nil {
			job.ExpiresAt = &ts
		}
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		job.ExitCode = &v
	}
	job.ApprovedBy = strings.TrimSpace(approvedBy)
	job.RejectedBy = strings.TrimSpace(rejectedBy)
	job.RejectionReason = strings.TrimSpace(rejectionReason)
	if approvalDeadline.Valid && approvalDeadline.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, approvalDeadline.String)
		if err == nil {
			job.ApprovalDeadline = &ts
		}
	}

	return &job, nil
}

// ApproveAsyncJob atomically transitions a waiting_approval job to running.
// Returns ErrAsyncJobConflict if the job is not in waiting_approval state (race-safe: uses UPDATE WHERE state=?).
func (s *Store) ApproveAsyncJob(jobID string) (*AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id required")
	}
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`UPDATE async_jobs
		 SET state = ?, started_at = ?, updated_at = ?
		 WHERE id = ? AND state = ?`,
		string(AsyncJobStateRunning), nowStr, nowStr,
		jobID, string(AsyncJobStateWaitingApproval),
	)
	if err != nil {
		return nil, fmt.Errorf("approve async job: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		existing, err := s.GetAsyncJob(jobID)
		if err != nil {
			return nil, err
		}
		return nil, &AsyncJobConflictError{
			JobID:        jobID,
			CurrentState: existing.State,
			Operation:    "approve",
		}
	}
	return s.GetAsyncJob(jobID)
}

// RejectAsyncJob atomically transitions a waiting_approval job to failed with the given reason.
// Returns ErrAsyncJobConflict if the job is not in waiting_approval state (race-safe).
func (s *Store) RejectAsyncJob(jobID, reason string) (*AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id required")
	}
	reason = strings.TrimSpace(reason)
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`UPDATE async_jobs
		 SET state = ?, status_reason = ?, rejection_reason = ?, finished_at = ?, updated_at = ?
		 WHERE id = ? AND state = ?`,
		string(AsyncJobStateFailed), reason, reason, nowStr, nowStr,
		jobID, string(AsyncJobStateWaitingApproval),
	)
	if err != nil {
		return nil, fmt.Errorf("reject async job: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		existing, err := s.GetAsyncJob(jobID)
		if err != nil {
			return nil, err
		}
		return nil, &AsyncJobConflictError{
			JobID:        jobID,
			CurrentState: existing.State,
			Operation:    "reject",
		}
	}
	return s.GetAsyncJob(jobID)
}

// ListExpiredWaitingApprovalJobs returns waiting_approval jobs whose expires_at has passed.
func (s *Store) ListExpiredWaitingApprovalJobs(now time.Time, limit int) ([]AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	limit = normalizeAsyncJobLimit(limit)
	rows, err := s.db.Query(`SELECT
		id, probe_id, workspace_id, request_id, command, args_json, level, state, status_reason, approval_id,
		exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
		approved_by, rejected_by, rejection_reason, approval_deadline
		FROM async_jobs
		WHERE state = ? AND expires_at IS NOT NULL AND expires_at != '' AND expires_at <= ?
		ORDER BY expires_at ASC
		LIMIT ?`,
		string(AsyncJobStateWaitingApproval),
		now.UTC().Format(time.RFC3339Nano),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AsyncJob, 0, limit)
	for rows.Next() {
		job, err := scanAsyncJob(rows)
		if err != nil {
			continue
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

// ExtendApprovalExpiry updates expires_at for a waiting_approval job without changing state.
func (s *Store) ExtendApprovalExpiry(jobID string, newExpiry time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store unavailable")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job id required")
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE async_jobs SET expires_at = ?, updated_at = ? WHERE id = ? AND state = ?`,
		newExpiry.UTC().Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		jobID,
		string(AsyncJobStateWaitingApproval),
	)
	return err
}

// ListAsyncJobsByWorkspace returns async jobs for a specific workspace.
// When workspaceID is empty it behaves like ListAsyncJobs (no filter).
func (s *Store) ListAsyncJobsByWorkspace(workspaceID string, limit int) ([]AsyncJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return s.ListAsyncJobs(limit)
	}
	limit = normalizeAsyncJobLimit(limit)
	rows, err := s.db.Query(`SELECT
		id, workspace_id, probe_id, request_id, command, args_json, level, state, status_reason, approval_id,
		exit_code, output, created_at, updated_at, started_at, finished_at, expires_at,
		approved_by, rejected_by, rejection_reason, approval_deadline
		FROM async_jobs
		WHERE workspace_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AsyncJob, 0, limit)
	for rows.Next() {
		job, err := scanAsyncJob(rows)
		if err != nil {
			continue
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

// GetAsyncJobCheckWorkspace fetches an async job and returns ErrWorkspaceMismatch
// if the workspace_id on the record doesn't match expectedWorkspace.
// When expectedWorkspace is empty the check is skipped (single-workspace mode).
func (s *Store) GetAsyncJobCheckWorkspace(id, expectedWorkspace string) (*AsyncJob, error) {
	job, err := s.GetAsyncJob(id)
	if err != nil {
		return nil, err
	}
	if expectedWorkspace == "" {
		return job, nil
	}
	if job.WorkspaceID != "" && job.WorkspaceID != expectedWorkspace {
		return nil, ErrWorkspaceMismatch
	}
	return job, nil
}
