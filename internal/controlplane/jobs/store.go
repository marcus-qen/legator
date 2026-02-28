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
		RunStatusRunning,
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

// CompleteRun finalizes a previously recorded job run.
func (s *Store) CompleteRun(runID, status string, exitCode *int, output string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run id required")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return fmt.Errorf("status required")
	}

	endedAt := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		jobID      string
		startedAtS string
	)
	if err := tx.QueryRow(`SELECT job_id, started_at FROM job_runs WHERE id = ?`, runID).Scan(&jobID, &startedAtS); err != nil {
		return err
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedAtS)
	if err != nil {
		return fmt.Errorf("parse run started_at: %w", err)
	}

	res, err := tx.Exec(`UPDATE job_runs SET ended_at = ?, status = ?, exit_code = ?, output = ? WHERE id = ? AND status = ?`,
		endedAt.Format(time.RFC3339Nano),
		status,
		nullableInt(exitCode),
		truncateOutput(output),
		runID,
		RunStatusRunning,
	)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}

	latestStartedAt := ""
	if err := tx.QueryRow(`SELECT COALESCE(MAX(started_at), '') FROM job_runs WHERE job_id = ?`, jobID).Scan(&latestStartedAt); err != nil {
		return err
	}

	if latestStartedAt == startedAt.Format(time.RFC3339Nano) {
		var (
			runningCount int
			failedCount  int
		)
		if err := tx.QueryRow(`SELECT
			SUM(CASE WHEN status = ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = ? THEN 1 ELSE 0 END)
			FROM job_runs
			WHERE job_id = ? AND started_at = ?`,
			RunStatusRunning,
			RunStatusFailed,
			jobID,
			latestStartedAt,
		).Scan(&runningCount, &failedCount); err != nil {
			return err
		}

		finalStatus := RunStatusSuccess
		switch {
		case runningCount > 0:
			finalStatus = RunStatusRunning
		case failedCount > 0:
			finalStatus = RunStatusFailed
		}

		if _, err := tx.Exec(`UPDATE jobs SET last_status = ?, updated_at = ? WHERE id = ? AND last_run_at = ?`,
			finalStatus,
			endedAt.Format(time.RFC3339Nano),
			jobID,
			latestStartedAt,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
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

// IsNotFound reports whether err is sql.ErrNoRows.
func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
