package jobs

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
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
		id           TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		command      TEXT NOT NULL,
		schedule     TEXT NOT NULL,
		target_kind  TEXT NOT NULL,
		target_value TEXT NOT NULL DEFAULT '',
		enabled      INTEGER NOT NULL DEFAULT 1,
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL,
		last_run_at  TEXT,
		last_status  TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create jobs table: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS job_runs (
		id         TEXT PRIMARY KEY,
		job_id     TEXT NOT NULL,
		probe_id   TEXT NOT NULL,
		request_id TEXT NOT NULL,
		started_at TEXT NOT NULL,
		ended_at   TEXT,
		status     TEXT NOT NULL,
		exit_code  INTEGER,
		output     TEXT NOT NULL DEFAULT '',
		FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create job_runs table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_enabled ON jobs(enabled)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_updated_at ON jobs(updated_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_job_runs_job_started ON job_runs(job_id, started_at DESC)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_job_runs_request_id ON job_runs(request_id)`)

	s := &Store{db: db}
	if err := s.pruneRunsOlderThan(runRetention); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prune job runs: %w", err)
	}

	return s, nil
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

	_, err := s.db.Exec(`INSERT INTO jobs (id, name, command, schedule, target_kind, target_value, enabled, created_at, updated_at, last_run_at, last_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		strings.TrimSpace(job.Name),
		strings.TrimSpace(job.Command),
		strings.TrimSpace(job.Schedule),
		job.Target.Kind,
		job.Target.Value,
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
		SET name = ?, command = ?, schedule = ?, target_kind = ?, target_value = ?, enabled = ?, updated_at = ?, last_status = ?
		WHERE id = ?`,
		strings.TrimSpace(job.Name),
		strings.TrimSpace(job.Command),
		strings.TrimSpace(job.Schedule),
		job.Target.Kind,
		job.Target.Value,
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
	row := s.db.QueryRow(`SELECT id, name, command, schedule, target_kind, target_value, enabled, created_at, updated_at, last_run_at, last_status
		FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

// ListJobs returns all jobs sorted by updated time (newest first).
func (s *Store) ListJobs() ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, name, command, schedule, target_kind, target_value, enabled, created_at, updated_at, last_run_at, last_status
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

	_, err = tx.Exec(`INSERT INTO job_runs (id, job_id, probe_id, request_id, started_at, ended_at, status, exit_code, output)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID,
		run.JobID,
		run.ProbeID,
		run.RequestID,
		run.StartedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(run.EndedAt),
		run.Status,
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

// MarkRunRunning transitions a run from pending to running.
func (s *Store) MarkRunRunning(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	return s.transitionRun(runID, []string{RunStatusPending}, RunStatusRunning, nil, "", false)
}

// CompleteRun finalizes a previously recorded job run.
func (s *Store) CompleteRun(runID, status string, exitCode *int, output string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	status = strings.TrimSpace(status)
	if status != RunStatusSuccess && status != RunStatusFailed {
		return fmt.Errorf("status must be success or failed")
	}

	return s.transitionRun(runID, []string{RunStatusRunning}, status, exitCode, output, true)
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
	return s.transitionRun(runID, []string{RunStatusPending, RunStatusRunning}, RunStatusCanceled, nil, reason, true)
}

// GetRun returns one run by id.
func (s *Store) GetRun(id string) (*JobRun, error) {
	row := s.db.QueryRow(`SELECT id, job_id, probe_id, request_id, started_at, ended_at, status, exit_code, output
		FROM job_runs WHERE id = ?`, id)
	return scanRun(row)
}

// ListActiveRunsByJob returns pending/running runs for the given job.
func (s *Store) ListActiveRunsByJob(jobID string) ([]JobRun, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("job id required")
	}

	rows, err := s.db.Query(`SELECT id, job_id, probe_id, request_id, started_at, ended_at, status, exit_code, output
		FROM job_runs
		WHERE job_id = ? AND status IN (?, ?)
		ORDER BY started_at DESC`, jobID, RunStatusPending, RunStatusRunning)
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

func (s *Store) transitionRun(runID string, fromStatuses []string, toStatus string, exitCode *int, output string, setEndedAt bool) error {
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
			output = CASE WHEN ? THEN ? ELSE output END
		WHERE id = ? AND status = ?`,
		endedAtValue,
		toStatus,
		nullableInt(exitCode),
		outputValue.Valid,
		outputValue.String,
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
		pendingCount  int
		runningCount  int
		failedCount   int
		canceledCount int
	)
	if err := tx.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
		FROM job_runs
		WHERE job_id = ? AND started_at = ?`,
		RunStatusPending,
		RunStatusRunning,
		RunStatusFailed,
		RunStatusCanceled,
		jobID,
		latestStartedAt,
	).Scan(&pendingCount, &runningCount, &failedCount, &canceledCount); err != nil {
		return err
	}

	finalStatus := RunStatusSuccess
	switch {
	case runningCount > 0:
		finalStatus = RunStatusRunning
	case pendingCount > 0:
		finalStatus = RunStatusPending
	case failedCount > 0:
		finalStatus = RunStatusFailed
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

	stmt := `SELECT id, job_id, probe_id, request_id, started_at, ended_at, status, exit_code, output FROM job_runs`
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
	)

	if err := s.Scan(
		&job.ID,
		&job.Name,
		&job.Command,
		&job.Schedule,
		&job.Target.Kind,
		&job.Target.Value,
		&enabled,
		&createdAt,
		&updatedAt,
		&lastRunAt,
		&job.LastStatus,
	); err != nil {
		return nil, err
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
		run       JobRun
		startedAt string
		endedAt   sql.NullString
		exitCode  sql.NullInt64
	)

	if err := s.Scan(
		&run.ID,
		&run.JobID,
		&run.ProbeID,
		&run.RequestID,
		&startedAt,
		&endedAt,
		&run.Status,
		&exitCode,
		&run.Output,
	); err != nil {
		return nil, err
	}

	run.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if endedAt.Valid && endedAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, endedAt.String)
		if err == nil {
			run.EndedAt = &ts
		}
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		run.ExitCode = &v
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
	case RunStatusPending, RunStatusRunning, RunStatusSuccess, RunStatusFailed, RunStatusCanceled:
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
