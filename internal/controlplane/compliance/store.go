package compliance

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Store persists compliance results in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a compliance database at dbPath.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open compliance db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	// Latest result per (check_id, probe_id) — upserted on each scan.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS compliance_results (
		id         TEXT PRIMARY KEY,
		check_id   TEXT NOT NULL,
		check_name TEXT NOT NULL,
		category   TEXT NOT NULL,
		severity   TEXT NOT NULL,
		probe_id   TEXT NOT NULL,
		status     TEXT NOT NULL,
		evidence   TEXT NOT NULL,
		timestamp  TEXT NOT NULL,
		UNIQUE(check_id, probe_id)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create compliance_results: %w", err)
	}

	// Historical results — one row per check run.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS compliance_history (
		id         TEXT PRIMARY KEY,
		check_id   TEXT NOT NULL,
		check_name TEXT NOT NULL,
		category   TEXT NOT NULL,
		severity   TEXT NOT NULL,
		probe_id   TEXT NOT NULL,
		status     TEXT NOT NULL,
		evidence   TEXT NOT NULL,
		timestamp  TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create compliance_history: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cr_probe_id   ON compliance_results(probe_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cr_check_id   ON compliance_results(check_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cr_status     ON compliance_results(status)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cr_category   ON compliance_results(category)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_ch_probe_id   ON compliance_history(probe_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_ch_timestamp  ON compliance_history(timestamp)`)

	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// UpsertResult inserts or replaces the latest result for a (check_id, probe_id) pair
// and appends a row to history.
func (s *Store) UpsertResult(r ComplianceResult) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	ts := r.Timestamp.UTC().Format(time.RFC3339Nano)

	_, err := s.db.Exec(`INSERT INTO compliance_results
		(id, check_id, check_name, category, severity, probe_id, status, evidence, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(check_id, probe_id) DO UPDATE SET
			id        = excluded.id,
			check_name= excluded.check_name,
			category  = excluded.category,
			severity  = excluded.severity,
			status    = excluded.status,
			evidence  = excluded.evidence,
			timestamp = excluded.timestamp`,
		r.ID, r.CheckID, r.CheckName, r.Category, r.Severity, r.ProbeID, r.Status, r.Evidence, ts,
	)
	if err != nil {
		return fmt.Errorf("upsert compliance result: %w", err)
	}

	histID := uuid.NewString()
	_, err = s.db.Exec(`INSERT INTO compliance_history
		(id, check_id, check_name, category, severity, probe_id, status, evidence, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		histID, r.CheckID, r.CheckName, r.Category, r.Severity, r.ProbeID, r.Status, r.Evidence, ts,
	)
	return err
}

// ListResults returns latest results, filtered by the given criteria.
func (s *Store) ListResults(filter ResultFilter) ([]ComplianceResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 500
	}

	where := []string{}
	args := []any{}

	if filter.ProbeID != "" {
		where = append(where, "probe_id = ?")
		args = append(args, filter.ProbeID)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.Category != "" {
		where = append(where, "category = ?")
		args = append(args, filter.Category)
	}
	if filter.CheckID != "" {
		where = append(where, "check_id = ?")
		args = append(args, filter.CheckID)
	}

	query := `SELECT id, check_id, check_name, category, severity, probe_id, status, evidence, timestamp
		FROM compliance_results`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, filter.Limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list compliance results: %w", err)
	}
	defer rows.Close()

	var out []ComplianceResult
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			continue
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Summary computes the fleet-wide compliance summary from latest results.
func (s *Store) Summary() (ComplianceSummary, error) {
	rows, err := s.db.Query(`SELECT category, status, COUNT(*) FROM compliance_results GROUP BY category, status`)
	if err != nil {
		return ComplianceSummary{}, fmt.Errorf("query summary: %w", err)
	}
	defer rows.Close()

	byCategory := map[string]*CategorySummary{}
	total := ComplianceSummary{ByCategory: map[string]CategorySummary{}}

	for rows.Next() {
		var cat, status string
		var count int
		if err := rows.Scan(&cat, &status, &count); err != nil {
			continue
		}
		if _, ok := byCategory[cat]; !ok {
			byCategory[cat] = &CategorySummary{Category: cat}
		}
		cs := byCategory[cat]
		cs.Total += count
		total.TotalChecks += count
		switch status {
		case StatusPass:
			cs.Passing += count
			total.Passing += count
		case StatusFail:
			cs.Failing += count
			total.Failing += count
		case StatusWarning:
			cs.Warning += count
			total.Warning += count
		default:
			cs.Unknown += count
			total.Unknown += count
		}
	}
	if err := rows.Err(); err != nil {
		return ComplianceSummary{}, err
	}

	// Count distinct probes in results
	row := s.db.QueryRow(`SELECT COUNT(DISTINCT probe_id) FROM compliance_results`)
	_ = row.Scan(&total.TotalProbes)

	// Score = passing / (total - unknown) * 100
	scored := total.Passing + total.Failing + total.Warning
	if scored > 0 {
		total.ScorePct = float64(total.Passing) / float64(scored) * 100
	}

	for cat, cs := range byCategory {
		scoredCat := cs.Passing + cs.Failing + cs.Warning
		if scoredCat > 0 {
			cs.ScorePct = float64(cs.Passing) / float64(scoredCat) * 100
		}
		total.ByCategory[cat] = *cs
	}

	return total, nil
}

// History returns historical results for a probe/check pair.
func (s *Store) History(probeID, checkID string, limit int) ([]ComplianceResult, error) {
	if limit <= 0 {
		limit = 50
	}
	args := []any{}
	where := []string{}
	if probeID != "" {
		where = append(where, "probe_id = ?")
		args = append(args, probeID)
	}
	if checkID != "" {
		where = append(where, "check_id = ?")
		args = append(args, checkID)
	}
	query := `SELECT id, check_id, check_name, category, severity, probe_id, status, evidence, timestamp
		FROM compliance_history`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ComplianceResult
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			continue
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// PurgeHistory deletes history older than the given duration.
func (s *Store) PurgeHistory(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	res, err := s.db.Exec(`DELETE FROM compliance_history WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanResult(s scanner) (*ComplianceResult, error) {
	var r ComplianceResult
	var ts string
	if err := s.Scan(&r.ID, &r.CheckID, &r.CheckName, &r.Category, &r.Severity, &r.ProbeID, &r.Status, &r.Evidence, &ts); err != nil {
		return nil, err
	}
	r.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return &r, nil
}
