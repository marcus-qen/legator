package compliance

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// initExportTable creates the compliance_exports table if it doesn't exist.
// Called from NewStore.
func initExportTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS compliance_exports (
		id         TEXT PRIMARY KEY,
		format     TEXT NOT NULL,
		status     TEXT NOT NULL,
		error_msg  TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		size_bytes INTEGER NOT NULL DEFAULT 0,
		probe_ids  TEXT NOT NULL DEFAULT '',
		category   TEXT NOT NULL DEFAULT '',
		since      TEXT NOT NULL DEFAULT '',
		until      TEXT NOT NULL DEFAULT '',
		data       BLOB NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create compliance_exports: %w", err)
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_ce_created_at ON compliance_exports(created_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_ce_format ON compliance_exports(format)`)
	return nil
}

// SaveExport persists a generated export record and its payload to the store.
func (s *Store) SaveExport(rec ExportRecord, data []byte) error {
	if rec.ID == "" {
		rec.ID = uuid.NewString()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	rec.SizeBytes = int64(len(data))

	probeIDs := strings.Join(rec.ProbeIDs, ",")

	_, err := s.db.Exec(`INSERT INTO compliance_exports
		(id, format, status, error_msg, created_at, size_bytes, probe_ids, category, since, until, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID,
		string(rec.Format),
		rec.Status,
		rec.ErrorMsg,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		rec.SizeBytes,
		probeIDs,
		rec.Category,
		rec.Since,
		rec.Until,
		data,
	)
	if err != nil {
		return fmt.Errorf("save export: %w", err)
	}
	return nil
}

// GetExport retrieves an export record and its payload by ID.
func (s *Store) GetExport(id string) (ExportRecord, []byte, error) {
	row := s.db.QueryRow(`SELECT id, format, status, error_msg, created_at, size_bytes,
		probe_ids, category, since, until, data
		FROM compliance_exports WHERE id = ?`, id)

	rec, data, err := scanExport(row)
	if err == sql.ErrNoRows {
		return ExportRecord{}, nil, fmt.Errorf("export %s not found", id)
	}
	return rec, data, err
}

// ListExports returns metadata for recent exports (no payload data).
func (s *Store) ListExports(limit int) ([]ExportRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, format, status, error_msg, created_at, size_bytes,
		probe_ids, category, since, until, ''
		FROM compliance_exports ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list exports: %w", err)
	}
	defer rows.Close()

	var out []ExportRecord
	for rows.Next() {
		rec, _, err := scanExport(rows)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// PurgeOldExports deletes exports older than olderThan duration.
func (s *Store) PurgeOldExports(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	res, err := s.db.Exec(`DELETE FROM compliance_exports WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge old exports: %w", err)
	}
	return res.RowsAffected()
}

type exportScanner interface {
	Scan(dest ...any) error
}

func scanExport(s exportScanner) (ExportRecord, []byte, error) {
	var rec ExportRecord
	var format, createdAt, probeIDs string
	var data []byte

	if err := s.Scan(
		&rec.ID, &format, &rec.Status, &rec.ErrorMsg, &createdAt, &rec.SizeBytes,
		&probeIDs, &rec.Category, &rec.Since, &rec.Until, &data,
	); err != nil {
		return ExportRecord{}, nil, err
	}

	rec.Format = ExportFormat(format)
	rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if probeIDs != "" {
		rec.ProbeIDs = strings.Split(probeIDs, ",")
	}
	return rec, data, nil
}

// ListHistory returns historical compliance results filtered by time range and other criteria.
func (s *Store) ListHistory(filter ExportFilter) ([]ComplianceResult, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 10000
	}

	where := []string{}
	args := []any{}

	if filter.Category != "" {
		where = append(where, "category = ?")
		args = append(args, filter.Category)
	}
	if len(filter.ProbeIDs) == 1 {
		where = append(where, "probe_id = ?")
		args = append(args, filter.ProbeIDs[0])
	} else if len(filter.ProbeIDs) > 1 {
		placeholders := make([]string, len(filter.ProbeIDs))
		for i, pid := range filter.ProbeIDs {
			placeholders[i] = "?"
			args = append(args, pid)
		}
		where = append(where, "probe_id IN ("+strings.Join(placeholders, ",")+")")
	}
	if !filter.Since.IsZero() {
		where = append(where, "timestamp >= ?")
		args = append(args, filter.Since.UTC().Format(time.RFC3339Nano))
	}
	if !filter.Until.IsZero() {
		where = append(where, "timestamp <= ?")
		args = append(args, filter.Until.UTC().Format(time.RFC3339Nano))
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
		return nil, fmt.Errorf("list history: %w", err)
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

// ExportRecordMetaJSON returns ExportRecord as JSON bytes (no data field).
func exportRecordToMap(rec ExportRecord) map[string]any {
	m := map[string]any{
		"id":         rec.ID,
		"format":     rec.Format,
		"status":     rec.Status,
		"created_at": rec.CreatedAt.UTC().Format(time.RFC3339),
		"size_bytes": rec.SizeBytes,
	}
	if rec.ErrorMsg != "" {
		m["error"] = rec.ErrorMsg
	}
	if len(rec.ProbeIDs) > 0 {
		m["probe_ids"] = rec.ProbeIDs
	}
	if rec.Category != "" {
		m["category"] = rec.Category
	}
	if rec.Since != "" {
		m["since"] = rec.Since
	}
	if rec.Until != "" {
		m["until"] = rec.Until
	}
	return m
}

// Ensure json package is used.
var _ = json.Marshal
