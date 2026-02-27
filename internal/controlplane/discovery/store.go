package discovery

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists discovery scan runs and candidates.
type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open discovery db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS discovery_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		cidr TEXT NOT NULL,
		started_at TEXT NOT NULL,
		completed_at TEXT,
		status TEXT NOT NULL,
		error TEXT
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create discovery_runs: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS discovery_candidates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id INTEGER NOT NULL,
		ip TEXT NOT NULL,
		hostname TEXT,
		open_ports_json TEXT NOT NULL,
		confidence TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES discovery_runs(id) ON DELETE CASCADE
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create discovery_candidates: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_discovery_runs_started ON discovery_runs(started_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_discovery_candidates_run ON discovery_candidates(run_id)`)

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) CreateRun(cidr string, startedAt time.Time) (*ScanRun, error) {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	result, err := s.db.Exec(`INSERT INTO discovery_runs (cidr, started_at, status) VALUES (?, ?, ?)`,
		strings.TrimSpace(cidr),
		startedAt.UTC().Format(time.RFC3339Nano),
		StatusRunning,
	)
	if err != nil {
		return nil, fmt.Errorf("insert discovery run: %w", err)
	}

	runID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read run id: %w", err)
	}

	return s.GetRun(runID)
}

func (s *Store) CompleteRun(runID int64, status string, scanErr string, completedAt time.Time) error {
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}

	result, err := s.db.Exec(`UPDATE discovery_runs
		SET completed_at = ?, status = ?, error = ?
		WHERE id = ?`,
		completedAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(status),
		nullString(scanErr),
		runID,
	)
	if err != nil {
		return fmt.Errorf("update discovery run: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ReplaceCandidates(runID int64, candidates []Candidate) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM discovery_candidates WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("delete old candidates: %w", err)
	}

	for _, candidate := range candidates {
		portsJSON, err := json.Marshal(candidate.OpenPorts)
		if err != nil {
			return fmt.Errorf("marshal open ports: %w", err)
		}

		if _, err := tx.Exec(`INSERT INTO discovery_candidates
			(run_id, ip, hostname, open_ports_json, confidence)
			VALUES (?, ?, ?, ?, ?)`,
			runID,
			strings.TrimSpace(candidate.IP),
			nullString(candidate.Hostname),
			string(portsJSON),
			strings.TrimSpace(candidate.Confidence),
		); err != nil {
			return fmt.Errorf("insert candidate: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) ListRuns(limit int) ([]ScanRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	rows, err := s.db.Query(`SELECT id, cidr, started_at, completed_at, status, error
		FROM discovery_runs
		ORDER BY started_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list discovery runs: %w", err)
	}
	defer rows.Close()

	runs := make([]ScanRun, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			continue
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

func (s *Store) GetRun(runID int64) (*ScanRun, error) {
	row := s.db.QueryRow(`SELECT id, cidr, started_at, completed_at, status, error
		FROM discovery_runs
		WHERE id = ?`, runID)
	return scanRun(row)
}

func (s *Store) ListCandidates(runID int64) ([]Candidate, error) {
	rows, err := s.db.Query(`SELECT id, run_id, ip, hostname, open_ports_json, confidence
		FROM discovery_candidates
		WHERE run_id = ?
		ORDER BY ip ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list discovery candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]Candidate, 0)
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			continue
		}
		candidates = append(candidates, *candidate)
	}

	return candidates, rows.Err()
}

func (s *Store) GetRunWithCandidates(runID int64) (*ScanResponse, error) {
	run, err := s.GetRun(runID)
	if err != nil {
		return nil, err
	}
	candidates, err := s.ListCandidates(runID)
	if err != nil {
		return nil, err
	}
	return &ScanResponse{Run: *run, Candidates: candidates}, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRun(row scanner) (*ScanRun, error) {
	var (
		run                       ScanRun
		startedAtRaw              string
		completedAtRaw, runErrRaw sql.NullString
	)

	if err := row.Scan(&run.ID, &run.CIDR, &startedAtRaw, &completedAtRaw, &run.Status, &runErrRaw); err != nil {
		return nil, err
	}

	run.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAtRaw)
	if completedAtRaw.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, completedAtRaw.String); err == nil {
			run.CompletedAt = &parsed
		}
	}
	if runErrRaw.Valid {
		run.Error = runErrRaw.String
	}
	return &run, nil
}

func scanCandidate(row scanner) (*Candidate, error) {
	var (
		candidate Candidate
		hostname  sql.NullString
		portsJSON string
	)

	if err := row.Scan(&candidate.ID, &candidate.RunID, &candidate.IP, &hostname, &portsJSON, &candidate.Confidence); err != nil {
		return nil, err
	}
	if hostname.Valid {
		candidate.Hostname = hostname.String
	}
	if strings.TrimSpace(portsJSON) != "" {
		_ = json.Unmarshal([]byte(portsJSON), &candidate.OpenPorts)
	}
	if candidate.OpenPorts == nil {
		candidate.OpenPorts = []int{}
	}
	return &candidate, nil
}

func nullString(v string) any {
	value := strings.TrimSpace(v)
	if value == "" {
		return nil
	}
	return value
}
