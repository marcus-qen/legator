package jobs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
)

const (
	maxRunOutputBytes = 10 * 1024
	runRetention      = 7 * 24 * time.Hour
	defaultRunLimit   = 50
	maxRunListLimit   = 500
)

var ErrInvalidRunTransition = errors.New("invalid run status transition")

// RunQuery controls filtering for job run history lookups.
type RunQuery struct {
	JobID         string
	ProbeID       string
	Status        string
	StartedAfter  *time.Time
	StartedBefore *time.Time
	Limit         int
}

// Store persists scheduled jobs and job run history in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a jobs database.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open jobs db: %w", err)
	}

	// SQLite pragmas (busy_timeout, WAL) are connection-scoped with modernc.
	// Keep a single pooled connection to ensure deterministic write behavior
	// under concurrent scheduler/result goroutines.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id                    TEXT PRIMARY KEY,
		name                  TEXT NOT NULL,
		command               TEXT NOT NULL,
		schedule              TEXT NOT NULL,
		target_kind           TEXT NOT NULL,
		target_value          TEXT NOT NULL DEFAULT '',
		retry_max_attempts    INTEGER,
		retry_initial_backoff TEXT,
		retry_multiplier      REAL,
		retry_max_backoff     TEXT,
		enabled               INTEGER NOT NULL DEFAULT 1,
		created_at            TEXT NOT NULL,
		updated_at            TEXT NOT NULL,
		last_run_at           TEXT,
		last_status           TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create jobs table: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS job_runs (
		id                 TEXT PRIMARY KEY,
		job_id             TEXT NOT NULL,
		probe_id           TEXT NOT NULL,
		request_id         TEXT NOT NULL,
		execution_id       TEXT NOT NULL DEFAULT '',
		attempt            INTEGER NOT NULL DEFAULT 1,
		max_attempts       INTEGER NOT NULL DEFAULT 1,
		retry_scheduled_at TEXT,
		started_at         TEXT NOT NULL,
		ended_at           TEXT,
		status             TEXT NOT NULL,
		admission_decision TEXT NOT NULL DEFAULT '',
		admission_reason   TEXT NOT NULL DEFAULT '',
		admission_rationale TEXT NOT NULL DEFAULT '',
		exit_code          INTEGER,
		output             TEXT NOT NULL DEFAULT '',
		FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create job_runs table: %w", err)
	}

	if err := ensureJobColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureRunColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_enabled ON jobs(enabled)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_updated_at ON jobs(updated_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_job_runs_job_started ON job_runs(job_id, started_at DESC)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_job_runs_request_id ON job_runs(request_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_job_runs_execution_attempt ON job_runs(execution_id, attempt)`)

	s := &Store{db: db}
	if err := s.pruneRunsOlderThan(runRetention); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prune job runs: %w", err)
	}

	if err := migration.EnsureVersion(db, 1); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure schema version: %w", err)
	}
	return s, nil
}

func ensureJobColumns(db *sql.DB) error {
	if err := ensureColumn(db, "jobs", "retry_max_attempts", "retry_max_attempts INTEGER"); err != nil {
		return fmt.Errorf("add jobs.retry_max_attempts: %w", err)
	}
	if err := ensureColumn(db, "jobs", "retry_initial_backoff", "retry_initial_backoff TEXT"); err != nil {
		return fmt.Errorf("add jobs.retry_initial_backoff: %w", err)
	}
	if err := ensureColumn(db, "jobs", "retry_multiplier", "retry_multiplier REAL"); err != nil {
		return fmt.Errorf("add jobs.retry_multiplier: %w", err)
	}
	if err := ensureColumn(db, "jobs", "retry_max_backoff", "retry_max_backoff TEXT"); err != nil {
		return fmt.Errorf("add jobs.retry_max_backoff: %w", err)
	}
	return nil
}

func ensureRunColumns(db *sql.DB) error {
	if err := ensureColumn(db, "job_runs", "execution_id", "execution_id TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add job_runs.execution_id: %w", err)
	}
	if err := ensureColumn(db, "job_runs", "attempt", "attempt INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("add job_runs.attempt: %w", err)
	}
	if err := ensureColumn(db, "job_runs", "max_attempts", "max_attempts INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("add job_runs.max_attempts: %w", err)
	}
	if err := ensureColumn(db, "job_runs", "retry_scheduled_at", "retry_scheduled_at TEXT"); err != nil {
		return fmt.Errorf("add job_runs.retry_scheduled_at: %w", err)
	}
	if err := ensureColumn(db, "job_runs", "admission_decision", "admission_decision TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add job_runs.admission_decision: %w", err)
	}
	if err := ensureColumn(db, "job_runs", "admission_reason", "admission_reason TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add job_runs.admission_reason: %w", err)
	}
	if err := ensureColumn(db, "job_runs", "admission_rationale", "admission_rationale TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add job_runs.admission_rationale: %w", err)
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	exists, err := hasColumn(db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, definition))
	return err
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			typeName  string
			notNull   int
			defaultV  sql.NullString
			primaryKV int
		)
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultV, &primaryKV); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateJob inserts a new scheduled job.
func (s *Store) CreateJob(job Job) (*Job, error) {
	if err := validateJob(job); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if job.ID == "" {
		job.ID = uuid.NewString()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now

	enabled := 0
	if job.Enabled {
		enabled = 1
	}

	_, err := s.db.Exec(`INSERT INTO jobs (id, name, command, schedule, target_kind, target_value, retry_max_attempts, retry_initial_backoff, retry_multiplier, retry_max_backoff, enabled, created_at, updated_at, last_run_at, last_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		strings.TrimSpace(job.Name),
		strings.TrimSpace(job.Command),
		strings.TrimSpace(job.Schedule),
		job.Target.Kind,
		job.Target.Value,
		nullableRetryMaxAttempts(job.RetryPolicy),
		nullableRetryDuration(job.RetryPolicy, func(p *RetryPolicy) string { return p.InitialBackoff }),
		nullableRetryMultiplier(job.RetryPolicy),
		nullableRetryDuration(job.RetryPolicy, func(p *RetryPolicy) string { return p.MaxBackoff }),
		enabled,
		job.CreatedAt.Format(time.RFC3339Nano),
		job.UpdatedAt.Format(time.RFC3339Nano),
		nullableTime(job.LastRunAt),
		job.LastStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	out := job
	return &out, nil
}

// UpdateJob updates an existing scheduled job.
func (s *Store) UpdateJob(job Job) (*Job, error) {
	if strings.TrimSpace(job.ID) == "" {
		return nil, fmt.Errorf("job id required")
	}
	if err := validateJob(job); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	enabled := 0
	if job.Enabled {
		enabled = 1
	}

	res, err := s.db.Exec(`UPDATE jobs
		SET name = ?, command = ?, schedule = ?, target_kind = ?, target_value = ?, retry_max_attempts = ?, retry_initial_backoff = ?, retry_multiplier = ?, retry_max_backoff = ?, enabled = ?, updated_at = ?, last_status = ?
		WHERE id = ?`,
		strings.TrimSpace(job.Name),
		strings.TrimSpace(job.Command),
		strings.TrimSpace(job.Schedule),
		job.Target.Kind,
		job.Target.Value,
		nullableRetryMaxAttempts(job.RetryPolicy),
		nullableRetryDuration(job.RetryPolicy, func(p *RetryPolicy) string { return p.InitialBackoff }),
		nullableRetryMultiplier(job.RetryPolicy),
		nullableRetryDuration(job.RetryPolicy, func(p *RetryPolicy) string { return p.MaxBackoff }),
		enabled,
		now.Format(time.RFC3339Nano),
		strings.TrimSpace(job.LastStatus),
		job.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update job: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	return s.GetJob(job.ID)
}

// SetEnabled flips a job's enabled state.
func (s *Store) SetEnabled(id string, enabled bool) (*Job, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("job id required")
	}

	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	res, err := s.db.Exec(`UPDATE jobs SET enabled = ?, updated_at = ? WHERE id = ?`, enabledInt, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, fmt.Errorf("set enabled: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	return s.GetJob(id)
}

// GetJob returns one job by id.
func (s *Store) GetJob(id string) (*Job, error) {
	row := s.db.QueryRow(`SELECT id, name, command, schedule, target_kind, target_value, retry_max_attempts, retry_initial_backoff, retry_multiplier, retry_max_backoff, enabled, created_at, updated_at, last_run_at, last_status
		FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

// ListJobs returns all jobs sorted by updated time (newest first).
func (s *Store) ListJobs() ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, name, command, schedule, target_kind, target_value, retry_max_attempts, retry_initial_backoff, retry_multiplier, retry_max_backoff, enabled, created_at, updated_at, last_run_at, last_status
		FROM jobs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			continue
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

// DeleteJob removes a job and its run history.
func (s *Store) DeleteJob(id string) error {
	res, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordRunStart inserts a running job execution record.
func (s *Store) RecordRunStart(run JobRun) (*JobRun, error) {
	if strings.TrimSpace(run.JobID) == "" {
		return nil, fmt.Errorf("job_id required")
	}
	if strings.TrimSpace(run.ProbeID) == "" {
		return nil, fmt.Errorf("probe_id required")
	}
	if strings.TrimSpace(run.RequestID) == "" {
		return nil, fmt.Errorf("request_id required")
	}

	now := time.Now().UTC()
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.ExecutionID = strings.TrimSpace(run.ExecutionID); run.ExecutionID == "" {
		run.ExecutionID = run.ID
	}
	if run.Attempt <= 0 {
		run.Attempt = 1
	}
	if run.MaxAttempts <= 0 {
		run.MaxAttempts = run.Attempt
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	if run.Status == "" {
		run.Status = RunStatusRunning
	}
	if !isKnownRunStatus(run.Status) {
		return nil, fmt.Errorf("invalid run status: %s", run.Status)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`INSERT INTO job_runs (id, job_id, probe_id, request_id, execution_id, attempt, max_attempts, retry_scheduled_at, started_at, ended_at, status, admission_decision, admission_reason, admission_rationale, exit_code, output)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID,
		run.JobID,
		run.ProbeID,
		run.RequestID,
		run.ExecutionID,
		run.Attempt,
		run.MaxAttempts,
		nullableTime(run.RetryScheduledAt),
		run.StartedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(run.EndedAt),
		run.Status,
		strings.TrimSpace(run.AdmissionDecision),
		strings.TrimSpace(run.AdmissionReason),
		serializeAdmissionRationale(run.AdmissionRationale),
		nullableInt(run.ExitCode),
		truncateOutput(run.Output),
	)
	if err != nil {
		return nil, fmt.Errorf("insert run: %w", err)
	}

	_, err = tx.Exec(`UPDATE jobs SET last_run_at = ?, last_status = ?, updated_at = ? WHERE id = ?`,
		run.StartedAt.UTC().Format(time.RFC3339Nano),
		run.Status,
		now.Format(time.RFC3339Nano),
		run.JobID,
	)
	if err != nil {
		return nil, fmt.Errorf("update job running status: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	out := run
	return &out, nil
}

// MarkRunPending transitions a run from queued to pending.
func (s *Store) MarkRunPending(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	return s.transitionRun(runID, []string{RunStatusQueued}, RunStatusPending, nil, "", false, nil)
}

// MarkRunRunning transitions a run from pending to running.
func (s *Store) MarkRunRunning(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	return s.transitionRun(runID, []string{RunStatusPending}, RunStatusRunning, nil, "", false, nil)
}

// MarkRunDenied transitions a queued/pending/running run to denied with rationale context.
func (s *Store) MarkRunDenied(runID, reason string, rationale any) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "admission denied by policy"
	}
	if err := s.transitionRun(runID, []string{RunStatusQueued, RunStatusPending, RunStatusRunning}, RunStatusDenied, nil, reason, true, nil); err != nil {
		return err
	}
	_, err := s.setRunAdmission(runID, string(AdmissionOutcomeDeny), reason, rationale, nil)
	return err
}

// UpdateQueuedRunAdmission refreshes queued-run rationale and next re-evaluation time.
func (s *Store) UpdateQueuedRunAdmission(runID, decision, reason string, rationale any, retryScheduledAt *time.Time) (*JobRun, error) {
	run, err := s.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if run.Status != RunStatusQueued {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidRunTransition, run.Status, RunStatusQueued)
	}
	return s.setRunAdmission(runID, decision, reason, rationale, retryScheduledAt)
}

// CompleteRun finalizes a previously recorded job run.
func (s *Store) CompleteRun(runID, status string, exitCode *int, output string) error {
	return s.CompleteRunWithRetry(runID, status, exitCode, output, nil)
}

// CompleteRunWithRetry finalizes a run and optionally records when a retry is scheduled.
func (s *Store) CompleteRunWithRetry(runID, status string, exitCode *int, output string, retryScheduledAt *time.Time) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	status = strings.TrimSpace(status)
	if status != RunStatusSuccess && status != RunStatusFailed {
		return fmt.Errorf("status must be success or failed")
	}

	return s.transitionRun(runID, []string{RunStatusRunning}, status, exitCode, output, true, retryScheduledAt)
}

// CancelRun transitions a run from pending/running to canceled.
func (s *Store) CancelRun(runID, reason string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "run canceled"
	}
	return s.transitionRun(runID, []string{RunStatusQueued, RunStatusPending, RunStatusRunning}, RunStatusCanceled, nil, reason, true, nil)
}

func (s *Store) setRunAdmission(runID, decision, reason string, rationale any, retryScheduledAt *time.Time) (*JobRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("run id required")
	}

	decision = strings.TrimSpace(decision)
	reason = strings.TrimSpace(reason)
	rationaleRaw, err := mustMarshalAdmissionRationale(rationale)
	if err != nil {
		return nil, err
	}

	res, err := s.db.Exec(`UPDATE job_runs
		SET admission_decision = ?,
			admission_reason = ?,
			admission_rationale = ?,
			retry_scheduled_at = ?
		WHERE id = ?`,
		decision,
		reason,
		string(rationaleRaw),
		nullableTime(retryScheduledAt),
		runID,
	)
	if err != nil {
		return nil, err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	return s.GetRun(runID)
}

// GetRun returns one run by id.
func (s *Store) GetRun(id string) (*JobRun, error) {
	row := s.db.QueryRow(`SELECT id, job_id, probe_id, request_id, execution_id, attempt, max_attempts, retry_scheduled_at, started_at, ended_at, status, admission_decision, admission_reason, admission_rationale, exit_code, output
		FROM job_runs WHERE id = ?`, id)
	return scanRun(row)
}

// ListActiveRunsByJob returns pending/running runs for the given job.
func (s *Store) ListActiveRunsByJob(jobID string) ([]JobRun, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id required")
	}

	rows, err := s.db.Query(`SELECT id, job_id, probe_id, request_id, execution_id, attempt, max_attempts, retry_scheduled_at, started_at, ended_at, status, admission_decision, admission_reason, admission_rationale, exit_code, output
		FROM job_runs
		WHERE job_id = ? AND status IN (?, ?, ?)
		ORDER BY started_at DESC`, jobID, RunStatusQueued, RunStatusPending, RunStatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]JobRun, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			continue
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func (s *Store) transitionRun(runID string, fromStatuses []string, toStatus string, exitCode *int, output string, setEndedAt bool, retryScheduledAt *time.Time) error {
	if !isKnownRunStatus(toStatus) {
		return fmt.Errorf("invalid status: %s", toStatus)
	}
	if len(fromStatuses) == 0 {
		return fmt.Errorf("from status required")
	}

	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		jobID      string
		startedAtS string
		current    string
	)
	if err := tx.QueryRow(`SELECT job_id, started_at, status FROM job_runs WHERE id = ?`, runID).Scan(&jobID, &startedAtS, &current); err != nil {
		return err
	}

	allowed := false
	for _, candidate := range fromStatuses {
		if current == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidRunTransition, current, toStatus)
	}

	endedAtValue := sql.NullString{}
	if setEndedAt {
		endedAtValue = sql.NullString{String: now.Format(time.RFC3339Nano), Valid: true}
	}
	outputValue := sql.NullString{}
	if strings.TrimSpace(output) != "" {
		outputValue = sql.NullString{String: truncateOutput(output), Valid: true}
	}

	res, err := tx.Exec(`UPDATE job_runs
		SET ended_at = COALESCE(?, ended_at),
			status = ?,
			exit_code = COALESCE(?, exit_code),
			output = CASE WHEN ? THEN ? ELSE output END,
			retry_scheduled_at = ?
		WHERE id = ? AND status = ?`,
		endedAtValue,
		toStatus,
		nullableInt(exitCode),
		outputValue.Valid,
		outputValue.String,
		nullableTime(retryScheduledAt),
		runID,
		current,
	)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		if err := tx.QueryRow(`SELECT status FROM job_runs WHERE id = ?`, runID).Scan(&current); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s -> %s", ErrInvalidRunTransition, current, toStatus)
	}

	startedAt, err := time.Parse(time.RFC3339Nano, startedAtS)
	if err != nil {
		return fmt.Errorf("parse run started_at: %w", err)
	}
	if err := s.updateJobStatusForLatestBatch(tx, jobID, startedAt.Format(time.RFC3339Nano), now); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) updateJobStatusForLatestBatch(tx *sql.Tx, jobID, runStartedAt string, now time.Time) error {
	latestStartedAt := ""
	if err := tx.QueryRow(`SELECT COALESCE(MAX(started_at), '') FROM job_runs WHERE job_id = ?`, jobID).Scan(&latestStartedAt); err != nil {
		return err
	}
	if latestStartedAt == "" || latestStartedAt != runStartedAt {
		return nil
	}

	var (
		queuedCount   int
		pendingCount  int
		runningCount  int
		failedCount   int
		deniedCount   int
		canceledCount int
	)
	if err := tx.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
		FROM job_runs
		WHERE job_id = ? AND started_at = ?`,
		RunStatusQueued,
		RunStatusPending,
		RunStatusRunning,
		RunStatusFailed,
		RunStatusDenied,
		RunStatusCanceled,
		jobID,
		latestStartedAt,
	).Scan(&queuedCount, &pendingCount, &runningCount, &failedCount, &deniedCount, &canceledCount); err != nil {
		return err
	}

	finalStatus := RunStatusSuccess
	switch {
	case runningCount > 0:
		finalStatus = RunStatusRunning
	case pendingCount > 0:
		finalStatus = RunStatusPending
	case queuedCount > 0:
		finalStatus = RunStatusQueued
	case failedCount > 0:
		finalStatus = RunStatusFailed
	case deniedCount > 0:
		finalStatus = RunStatusDenied
	case canceledCount > 0:
		finalStatus = RunStatusCanceled
	}

	_, err := tx.Exec(`UPDATE jobs SET last_status = ?, updated_at = ? WHERE id = ? AND last_run_at = ?`,
		finalStatus,
		now.Format(time.RFC3339Nano),
		jobID,
		latestStartedAt,
	)
	return err
}

// ListRunsByJob returns the most recent runs for one job.
func (s *Store) ListRunsByJob(jobID string, limit int) ([]JobRun, error) {
	return s.ListRuns(RunQuery{JobID: jobID, Limit: limit})
}

// ListRuns returns recent runs using optional filters.
func (s *Store) ListRuns(query RunQuery) ([]JobRun, error) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 6)

	if jobID := strings.TrimSpace(query.JobID); jobID != "" {
		clauses = append(clauses, "job_id = ?")
		args = append(args, jobID)
	}
	if probeID := strings.TrimSpace(query.ProbeID); probeID != "" {
		clauses = append(clauses, "probe_id = ?")
		args = append(args, probeID)
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	if query.StartedAfter != nil {
		clauses = append(clauses, "started_at >= ?")
		args = append(args, query.StartedAfter.UTC().Format(time.RFC3339Nano))
	}
	if query.StartedBefore != nil {
		clauses = append(clauses, "started_at <= ?")
		args = append(args, query.StartedBefore.UTC().Format(time.RFC3339Nano))
	}

	stmt := `SELECT id, job_id, probe_id, request_id, execution_id, attempt, max_attempts, retry_scheduled_at, started_at, ended_at, status, admission_decision, admission_reason, admission_rationale, exit_code, output FROM job_runs`
	if len(clauses) > 0 {
		stmt += ` WHERE ` + strings.Join(clauses, " AND ")
	}
	stmt += ` ORDER BY started_at DESC LIMIT ?`
	limit := normalizeRunLimit(query.Limit)
	args = append(args, limit)

	rows, err := s.db.Query(stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]JobRun, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			continue
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

func normalizeRunLimit(limit int) int {
	if limit <= 0 {
		return defaultRunLimit
	}
	if limit > maxRunListLimit {
		return maxRunListLimit
	}
	return limit
}

func (s *Store) pruneRunsOlderThan(age time.Duration) error {
	cutoff := time.Now().UTC().Add(-age).Format(time.RFC3339Nano)
	_, err := s.db.Exec(`DELETE FROM job_runs WHERE started_at < ?`, cutoff)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(s scanner) (*Job, error) {
	var (
		job                  Job
		enabled              int
		createdAt, updatedAt string
		lastRunAt            sql.NullString
		retryMaxAttempts     sql.NullInt64
		retryInitialBackoff  sql.NullString
		retryMultiplier      sql.NullFloat64
		retryMaxBackoff      sql.NullString
	)

	if err := s.Scan(
		&job.ID,
		&job.Name,
		&job.Command,
		&job.Schedule,
		&job.Target.Kind,
		&job.Target.Value,
		&retryMaxAttempts,
		&retryInitialBackoff,
		&retryMultiplier,
		&retryMaxBackoff,
		&enabled,
		&createdAt,
		&updatedAt,
		&lastRunAt,
		&job.LastStatus,
	); err != nil {
		return nil, err
	}

	if retryMaxAttempts.Valid || retryInitialBackoff.Valid || retryMultiplier.Valid || retryMaxBackoff.Valid {
		rp := &RetryPolicy{}
		if retryMaxAttempts.Valid {
			rp.MaxAttempts = int(retryMaxAttempts.Int64)
		}
		if retryInitialBackoff.Valid {
			rp.InitialBackoff = retryInitialBackoff.String
		}
		if retryMultiplier.Valid {
			rp.Multiplier = retryMultiplier.Float64
		}
		if retryMaxBackoff.Valid {
			rp.MaxBackoff = retryMaxBackoff.String
		}
		job.RetryPolicy = rp
	}

	job.Enabled = enabled == 1
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if lastRunAt.Valid && lastRunAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, lastRunAt.String)
		if err == nil {
			job.LastRunAt = &ts
		}
	}
	return &job, nil
}

func scanRun(s scanner) (*JobRun, error) {
	var (
		run               JobRun
		startedAt         string
		endedAt           sql.NullString
		retryScheduledAt  sql.NullString
		admissionDecision sql.NullString
		admissionReason   sql.NullString
		admissionRationale sql.NullString
		exitCode          sql.NullInt64
	)

	if err := s.Scan(
		&run.ID,
		&run.JobID,
		&run.ProbeID,
		&run.RequestID,
		&run.ExecutionID,
		&run.Attempt,
		&run.MaxAttempts,
		&retryScheduledAt,
		&startedAt,
		&endedAt,
		&run.Status,
		&admissionDecision,
		&admissionReason,
		&admissionRationale,
		&exitCode,
		&run.Output,
	); err != nil {
		return nil, err
	}

	run.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if retryScheduledAt.Valid && retryScheduledAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, retryScheduledAt.String)
		if err == nil {
			run.RetryScheduledAt = &ts
		}
	}
	if endedAt.Valid && endedAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, endedAt.String)
		if err == nil {
			run.EndedAt = &ts
		}
	}
	if admissionDecision.Valid {
		run.AdmissionDecision = strings.TrimSpace(admissionDecision.String)
	}
	if admissionReason.Valid {
		run.AdmissionReason = strings.TrimSpace(admissionReason.String)
	}
	if admissionRationale.Valid {
		raw := strings.TrimSpace(admissionRationale.String)
		if raw != "" {
			run.AdmissionRationale = json.RawMessage(raw)
		}
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		run.ExitCode = &v
	}
	if run.Attempt <= 0 {
		run.Attempt = 1
	}
	if run.MaxAttempts <= 0 {
		run.MaxAttempts = run.Attempt
	}
	if strings.TrimSpace(run.ExecutionID) == "" {
		run.ExecutionID = run.ID
	}
	return &run, nil
}

func validateJob(job Job) error {
	if strings.TrimSpace(job.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(job.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if strings.TrimSpace(job.Schedule) == "" {
		return fmt.Errorf("schedule is required")
	}

	switch job.Target.Kind {
	case TargetKindProbe:
		if strings.TrimSpace(job.Target.Value) == "" {
			return fmt.Errorf("target.value is required for probe target")
		}
	case TargetKindTag:
		if strings.TrimSpace(job.Target.Value) == "" {
			return fmt.Errorf("target.value is required for tag target")
		}
	case TargetKindAll:
		// no value required
	default:
		return fmt.Errorf("invalid target kind: %s", job.Target.Kind)
	}

	if err := validateRetryPolicy(job.RetryPolicy); err != nil {
		return err
	}

	return nil
}

func nullableTime(ts *time.Time) sql.NullString {
	if ts == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: ts.UTC().Format(time.RFC3339Nano), Valid: true}
}

func nullableInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

func nullableRetryMaxAttempts(policy *RetryPolicy) sql.NullInt64 {
	if policy == nil || policy.MaxAttempts <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(policy.MaxAttempts), Valid: true}
}

func nullableRetryMultiplier(policy *RetryPolicy) sql.NullFloat64 {
	if policy == nil || policy.Multiplier <= 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: policy.Multiplier, Valid: true}
}

func nullableRetryDuration(policy *RetryPolicy, get func(*RetryPolicy) string) sql.NullString {
	if policy == nil || get == nil {
		return sql.NullString{}
	}
	value := strings.TrimSpace(get(policy))
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func mustMarshalAdmissionRationale(rationale any) (json.RawMessage, error) {
	if rationale == nil {
		return nil, nil
	}
	switch typed := rationale.(type) {
	case json.RawMessage:
		trimmed := strings.TrimSpace(string(typed))
		if trimmed == "" {
			return nil, nil
		}
		return json.RawMessage(trimmed), nil
	case []byte:
		trimmed := strings.TrimSpace(string(typed))
		if trimmed == "" {
			return nil, nil
		}
		return json.RawMessage(trimmed), nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, nil
		}
		if json.Valid([]byte(trimmed)) {
			return json.RawMessage(trimmed), nil
		}
		encoded, err := json.Marshal(trimmed)
		if err != nil {
			return nil, fmt.Errorf("marshal admission rationale: %w", err)
		}
		return json.RawMessage(encoded), nil
	default:
		encoded, err := json.Marshal(rationale)
		if err != nil {
			return nil, fmt.Errorf("marshal admission rationale: %w", err)
		}
		return json.RawMessage(encoded), nil
	}
}

func serializeAdmissionRationale(rationale json.RawMessage) string {
	trimmed := strings.TrimSpace(string(rationale))
	if trimmed == "" {
		return ""
	}
	if !json.Valid([]byte(trimmed)) {
		encoded, err := json.Marshal(trimmed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
	return trimmed
}

func truncateOutput(output string) string {
	if len(output) <= maxRunOutputBytes {
		return output
	}
	if maxRunOutputBytes <= 16 {
		return output[:maxRunOutputBytes]
	}
	return output[:maxRunOutputBytes-16] + "\n...[truncated]"
}

func isKnownRunStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case RunStatusQueued, RunStatusPending, RunStatusRunning, RunStatusSuccess, RunStatusFailed, RunStatusCanceled, RunStatusDenied:
		return true
	default:
		return false
	}
}

// IsNotFound reports whether err is sql.ErrNoRows.
func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// IsInvalidRunTransition reports whether err is an invalid run status transition.
func IsInvalidRunTransition(err error) bool {
	return errors.Is(err, ErrInvalidRunTransition)
}
