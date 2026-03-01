package reliability

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
)

// IncidentStore persists incidents and their timelines in SQLite.
type IncidentStore struct {
	db *sql.DB
}

// NewIncidentStore opens (or creates) an incident store at dbPath.
func NewIncidentStore(dbPath string) (*IncidentStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open incident db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS incidents (
		id               TEXT PRIMARY KEY,
		title            TEXT NOT NULL,
		severity         TEXT NOT NULL,
		status           TEXT NOT NULL DEFAULT 'open',
		affected_probes  TEXT NOT NULL DEFAULT '[]',
		start_time       TEXT NOT NULL,
		end_time         TEXT,
		root_cause       TEXT NOT NULL DEFAULT '',
		resolution       TEXT NOT NULL DEFAULT '',
		created_at       TEXT NOT NULL,
		updated_at       TEXT NOT NULL,
		deleted_at       TEXT
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create incidents table: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS incident_timeline (
		id              TEXT PRIMARY KEY,
		incident_id     TEXT NOT NULL,
		timestamp       TEXT NOT NULL,
		type            TEXT NOT NULL,
		description     TEXT NOT NULL DEFAULT '',
		audit_event_id  TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create incident_timeline table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_status ON incidents(status)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_severity ON incidents(severity)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_start_time ON incidents(start_time)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_timeline_incident_id ON incident_timeline(incident_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_timeline_timestamp ON incident_timeline(timestamp)`)

	if err := migration.EnsureVersion(db, 1); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure schema version: %w", err)
	}
	return &IncidentStore{db: db}, nil
}

// Close closes the underlying database.
func (is *IncidentStore) Close() error {
	if is == nil || is.db == nil {
		return nil
	}
	return is.db.Close()
}

// Create persists a new incident and returns it with generated fields filled in.
func (is *IncidentStore) Create(inc Incident) (Incident, error) {
	now := time.Now().UTC()
	if inc.ID == "" {
		inc.ID = uuid.NewString()
	}
	if inc.Status == "" {
		inc.Status = StatusOpen
	}
	if inc.StartTime.IsZero() {
		inc.StartTime = now
	}
	inc.CreatedAt = now
	inc.UpdatedAt = now
	if inc.AffectedProbes == nil {
		inc.AffectedProbes = []string{}
	}

	probesJSON, err := json.Marshal(inc.AffectedProbes)
	if err != nil {
		probesJSON = []byte("[]")
	}

	var endTimeStr *string
	if inc.EndTime != nil {
		s := inc.EndTime.UTC().Format(time.RFC3339Nano)
		endTimeStr = &s
	}

	_, err = is.db.Exec(`INSERT INTO incidents
		(id, title, severity, status, affected_probes, start_time, end_time, root_cause, resolution, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inc.ID,
		inc.Title,
		string(inc.Severity),
		string(inc.Status),
		string(probesJSON),
		inc.StartTime.UTC().Format(time.RFC3339Nano),
		endTimeStr,
		inc.RootCause,
		inc.Resolution,
		inc.CreatedAt.Format(time.RFC3339Nano),
		inc.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Incident{}, fmt.Errorf("insert incident: %w", err)
	}
	return inc, nil
}

// Get retrieves an incident by ID (excludes soft-deleted records).
func (is *IncidentStore) Get(id string) (Incident, bool, error) {
	row := is.db.QueryRow(`SELECT id, title, severity, status, affected_probes, start_time, end_time,
		root_cause, resolution, created_at, updated_at
		FROM incidents WHERE id = ? AND deleted_at IS NULL`, id)
	inc, err := scanIncident(row)
	if err == sql.ErrNoRows {
		return Incident{}, false, nil
	}
	if err != nil {
		return Incident{}, false, err
	}
	return inc, true, nil
}

// List returns incidents matching the filter, ordered by start_time descending.
func (is *IncidentStore) List(f IncidentFilter) ([]Incident, error) {
	query := `SELECT id, title, severity, status, affected_probes, start_time, end_time,
		root_cause, resolution, created_at, updated_at
		FROM incidents WHERE deleted_at IS NULL`
	var args []any

	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, string(f.Status))
	}
	if f.Severity != "" {
		query += " AND severity = ?"
		args = append(args, string(f.Severity))
	}
	if !f.From.IsZero() {
		query += " AND start_time >= ?"
		args = append(args, f.From.UTC().Format(time.RFC3339Nano))
	}
	if !f.To.IsZero() {
		query += " AND start_time <= ?"
		args = append(args, f.To.UTC().Format(time.RFC3339Nano))
	}
	query += " ORDER BY start_time DESC"

	rows, err := is.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			continue
		}
		// Probe filter: post-filter since JSON array can't be filtered cleanly in SQL
		if f.Probe != "" {
			found := false
			for _, p := range inc.AffectedProbes {
				if p == f.Probe {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, inc)
	}
	if out == nil {
		out = []Incident{}
	}
	return out, rows.Err()
}

// Update applies a partial update to an incident.
func (is *IncidentStore) Update(id string, upd IncidentUpdate) (Incident, error) {
	now := time.Now().UTC()
	setClauses := []string{"updated_at = ?"}
	args := []any{now.Format(time.RFC3339Nano)}

	if upd.Status != nil {
		setClauses = append(setClauses, "status = ?")
		args = append(args, string(*upd.Status))
	}
	if upd.Title != nil {
		setClauses = append(setClauses, "title = ?")
		args = append(args, *upd.Title)
	}
	if upd.EndTime != nil {
		setClauses = append(setClauses, "end_time = ?")
		args = append(args, upd.EndTime.UTC().Format(time.RFC3339Nano))
	}
	if upd.RootCause != nil {
		setClauses = append(setClauses, "root_cause = ?")
		args = append(args, *upd.RootCause)
	}
	if upd.Resolution != nil {
		setClauses = append(setClauses, "resolution = ?")
		args = append(args, *upd.Resolution)
	}

	args = append(args, id)
	result, err := is.db.Exec(
		fmt.Sprintf("UPDATE incidents SET %s WHERE id = ? AND deleted_at IS NULL", strings.Join(setClauses, ", ")),
		args...,
	)
	if err != nil {
		return Incident{}, fmt.Errorf("update incident: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return Incident{}, fmt.Errorf("incident not found: %s", id)
	}

	inc, found, err := is.Get(id)
	if err != nil {
		return Incident{}, err
	}
	if !found {
		return Incident{}, fmt.Errorf("incident not found after update: %s", id)
	}
	return inc, nil
}

// SoftDelete marks an incident as deleted without removing it from the database.
func (is *IncidentStore) SoftDelete(id string) error {
	now := time.Now().UTC()
	result, err := is.db.Exec(
		"UPDATE incidents SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL",
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return fmt.Errorf("soft delete incident: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("incident not found: %s", id)
	}
	return nil
}

// AddTimelineEntry appends an entry to the incident timeline.
func (is *IncidentStore) AddTimelineEntry(entry TimelineEntry) (TimelineEntry, error) {
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	_, err := is.db.Exec(`INSERT INTO incident_timeline
		(id, incident_id, timestamp, type, description, audit_event_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		entry.ID,
		entry.IncidentID,
		entry.Timestamp.UTC().Format(time.RFC3339Nano),
		string(entry.Type),
		entry.Description,
		entry.AuditEventID,
	)
	if err != nil {
		return TimelineEntry{}, fmt.Errorf("insert timeline entry: %w", err)
	}
	return entry, nil
}

// GetTimeline returns all timeline entries for an incident, ordered by timestamp ascending.
func (is *IncidentStore) GetTimeline(incidentID string) ([]TimelineEntry, error) {
	rows, err := is.db.Query(`SELECT id, incident_id, timestamp, type, description, audit_event_id
		FROM incident_timeline
		WHERE incident_id = ?
		ORDER BY timestamp ASC, id ASC`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TimelineEntry
	for rows.Next() {
		e, err := scanTimelineEntry(rows)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	if out == nil {
		out = []TimelineEntry{}
	}
	return out, rows.Err()
}

// ── Scanning helpers ──────────────────────────────────────────

type incidentScanner interface {
	Scan(dest ...any) error
}

func scanIncident(s incidentScanner) (Incident, error) {
	var (
		inc        Incident
		probesJSON string
		startTime  string
		endTime    *string
		createdAt  string
		updatedAt  string
	)
	err := s.Scan(
		&inc.ID, &inc.Title, (*string)(&inc.Severity), (*string)(&inc.Status),
		&probesJSON, &startTime, &endTime,
		&inc.RootCause, &inc.Resolution,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return Incident{}, err
	}

	_ = json.Unmarshal([]byte(probesJSON), &inc.AffectedProbes)
	if inc.AffectedProbes == nil {
		inc.AffectedProbes = []string{}
	}

	inc.StartTime, _ = time.Parse(time.RFC3339Nano, startTime)
	inc.StartTime = inc.StartTime.UTC()

	if endTime != nil && *endTime != "" {
		t, _ := time.Parse(time.RFC3339Nano, *endTime)
		t = t.UTC()
		inc.EndTime = &t
	}

	inc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	inc.CreatedAt = inc.CreatedAt.UTC()
	inc.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	inc.UpdatedAt = inc.UpdatedAt.UTC()

	return inc, nil
}

type timelineEntryScanner interface {
	Scan(dest ...any) error
}

func scanTimelineEntry(s timelineEntryScanner) (TimelineEntry, error) {
	var (
		e  TimelineEntry
		ts string
	)
	err := s.Scan(
		&e.ID, &e.IncidentID, &ts, (*string)(&e.Type),
		&e.Description, &e.AuditEventID,
	)
	if err != nil {
		return TimelineEntry{}, err
	}
	e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	e.Timestamp = e.Timestamp.UTC()
	return e, nil
}
