package audit

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Store provides persistent audit log storage backed by SQLite.
// It wraps the in-memory Log and syncs events to disk.
type Store struct {
	db  *sql.DB
	log *Log // in-memory cache for fast queries
}

// NewStore opens (or creates) a SQLite-backed audit store.
func NewStore(dbPath string, memoryLimit int) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// WAL mode for concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	// Create table
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS audit_events (
		id         TEXT PRIMARY KEY,
		timestamp  TEXT NOT NULL,
		type       TEXT NOT NULL,
		probe_id   TEXT,
		actor      TEXT,
		summary    TEXT,
		detail     TEXT,
		before_val TEXT,
		after_val  TEXT
	)`); err != nil {
		db.Close()
		return nil, err
	}

	// Index for common queries
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_probe ON audit_events(probe_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_type ON audit_events(type)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_events(timestamp)`)

	s := &Store{
		db:  db,
		log: NewLog(memoryLimit),
	}

	// Load recent events into memory cache
	if err := s.loadRecent(memoryLimit); err != nil {
		_ = err // Non-fatal — store still works
	}

	return s, nil
}

// enrichEvent fills in ID and Timestamp if missing.
func enrichEvent(evt *Event) {
	if evt.ID == "" {
		evt.ID = uuid.New().String()
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
}

// Record persists an event to both memory and disk.
func (s *Store) Record(evt Event) {
	enrichEvent(&evt)
	s.log.Record(evt)
	_ = s.persist(evt)
}

// Emit is a convenience for recording a new event with minimal args.
func (s *Store) Emit(typ EventType, probeID, actor, summary string) {
	s.Record(Event{
		Type:    typ,
		ProbeID: probeID,
		Actor:   actor,
		Summary: summary,
	})
}

// Query delegates to the in-memory cache for fast reads.
func (s *Store) Query(f Filter) []Event {
	return s.log.Query(f)
}

// Recent returns the N most recent events from memory.
func (s *Store) Recent(n int) []Event {
	return s.log.Recent(n)
}

// Count returns the total persisted event count.
func (s *Store) Count() int {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&count)
	if err != nil {
		return s.log.Count()
	}
	return count
}

// QueryPersisted searches the SQLite store directly (for older events not in memory).
func (s *Store) QueryPersisted(f Filter) ([]Event, error) {
	query := "SELECT id, timestamp, type, probe_id, actor, summary, detail, before_val, after_val FROM audit_events WHERE 1=1"
	var args []any

	if f.ProbeID != "" {
		query += " AND probe_id = ?"
		args = append(args, f.ProbeID)
	}
	if f.Type != "" {
		query += " AND type = ?"
		args = append(args, string(f.Type))
	}
	if !f.Since.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, f.Since.Format(time.RFC3339Nano))
	}

	query += " ORDER BY timestamp DESC"

	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var evt Event
		var ts, detail, before, after string
		err := rows.Scan(&evt.ID, &ts, &evt.Type, &evt.ProbeID, &evt.Actor, &evt.Summary, &detail, &before, &after)
		if err != nil {
			continue
		}
		evt.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		if detail != "" && detail != "null" {
			json.Unmarshal([]byte(detail), &evt.Detail)
		}
		if before != "" && before != "null" {
			json.Unmarshal([]byte(before), &evt.Before)
		}
		if after != "" && after != "null" {
			json.Unmarshal([]byte(after), &evt.After)
		}
		events = append(events, evt)
	}
	return events, nil
}

// Close shuts down the store.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) persist(evt Event) error {
	detail, _ := json.Marshal(evt.Detail)
	before, _ := json.Marshal(evt.Before)
	after, _ := json.Marshal(evt.After)

	_, err := s.db.Exec(`INSERT OR IGNORE INTO audit_events (id, timestamp, type, probe_id, actor, summary, detail, before_val, after_val)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		evt.ID,
		evt.Timestamp.Format(time.RFC3339Nano),
		string(evt.Type),
		evt.ProbeID,
		evt.Actor,
		evt.Summary,
		string(detail),
		string(before),
		string(after),
	)
	return err
}

func (s *Store) loadRecent(limit int) error {
	events, err := s.QueryPersisted(Filter{Limit: limit})
	if err != nil {
		return err
	}

	// Load in reverse order (oldest first) so memory log is correctly ordered
	for i := len(events) - 1; i >= 0; i-- {
		s.log.Record(events[i])
	}
	return nil
}
