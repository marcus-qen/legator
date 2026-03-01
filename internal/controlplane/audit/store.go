package audit

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
)

// Store provides persistent audit log storage backed by SQLite.
// It wraps the in-memory Log and syncs events to disk.
type Store struct {
	db          *sql.DB
	log         *Log // in-memory cache for fast queries
	memoryLimit int
	mu          sync.RWMutex
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
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
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
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_probe ON audit_events(probe_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_type ON audit_events(type)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_events(timestamp)`)

	s := &Store{
		db:          db,
		log:         NewLog(memoryLimit),
		memoryLimit: memoryLimit,
	}

	// Load recent events into memory cache
	if err := s.loadRecent(memoryLimit); err != nil {
		_ = err // Non-fatal — store still works
	}

	if err := migration.EnsureVersion(db, 1); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure schema version: %w", err)
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

	s.mu.RLock()
	s.log.Record(evt)
	s.mu.RUnlock()

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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.log.Query(f)
}

// Recent returns the N most recent events from memory.
func (s *Store) Recent(n int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.log.Recent(n)
}

// Count returns the total persisted event count.
func (s *Store) Count() int {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&count)
	if err != nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.log.Count()
	}
	return count
}

// QueryPersisted searches the SQLite store directly (for older events not in memory).
func (s *Store) QueryPersisted(f Filter) ([]Event, error) {
	query, args, err := s.buildPersistedQuery(f, true, false)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		evt, err := scanEvent(rows)
		if err != nil {
			continue
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// StreamJSONL streams matching events as newline-delimited JSON.
func (s *Store) StreamJSONL(ctx context.Context, w io.Writer, f Filter) error {
	query, args, err := s.buildPersistedQuery(f, false, false)
	if err != nil {
		return err
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	enc := json.NewEncoder(w)
	for rows.Next() {
		evt, err := scanEvent(rows)
		if err != nil {
			continue
		}
		if err := enc.Encode(evt); err != nil {
			return err
		}
	}
	return rows.Err()
}

// StreamCSV streams matching events as CSV.
func (s *Store) StreamCSV(ctx context.Context, w io.Writer, f Filter) error {
	query, args, err := s.buildPersistedQuery(f, false, true)
	if err != nil {
		return err
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "timestamp", "type", "probe_id", "actor", "summary"}); err != nil {
		return err
	}

	for rows.Next() {
		var id, ts, typ, probeID, actor, summary string
		if err := rows.Scan(&id, &ts, &typ, &probeID, &actor, &summary); err != nil {
			continue
		}
		if err := cw.Write([]string{id, ts, typ, probeID, actor, summary}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	cw.Flush()
	return cw.Error()
}

// Purge deletes persisted events older than now - olderThan and returns deleted row count.
func (s *Store) Purge(olderThan time.Duration) (int64, error) {
	if olderThan < 0 {
		return 0, errors.New("olderThan must be >= 0")
	}

	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	res, err := s.db.Exec("DELETE FROM audit_events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, err
	}

	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	if deleted > 0 {
		if err := s.loadRecent(s.memoryLimit); err != nil {
			return deleted, err
		}
	}

	return deleted, nil
}

// PurgeLoop periodically applies retention to remove old audit events.
func (s *Store) PurgeLoop(ctx context.Context, retention time.Duration, interval time.Duration) {
	if retention <= 0 || interval <= 0 {
		return
	}

	_, _ = s.Purge(retention)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.Purge(retention)
		}
	}
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

	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = NewLog(s.memoryLimit)

	// Load in reverse order (oldest first) so memory log is correctly ordered
	for i := len(events) - 1; i >= 0; i-- {
		s.log.Record(events[i])
	}
	return nil
}

func (s *Store) buildPersistedQuery(f Filter, includeLimit bool, csvMode bool) (string, []any, error) {
	query := "SELECT id, timestamp, type, probe_id, actor, summary, detail, before_val, after_val FROM audit_events WHERE 1=1"
	if csvMode {
		query = "SELECT id, timestamp, type, probe_id, actor, summary FROM audit_events WHERE 1=1"
	}
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
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		query += " AND timestamp <= ?"
		args = append(args, f.Until.UTC().Format(time.RFC3339Nano))
	}
	if f.Cursor != "" {
		var cursorTS string
		err := s.db.QueryRow("SELECT timestamp FROM audit_events WHERE id = ?", f.Cursor).Scan(&cursorTS)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				query += " AND 1=0"
			} else {
				return "", nil, err
			}
		} else {
			query += " AND (timestamp < ? OR (timestamp = ? AND id < ?))"
			args = append(args, cursorTS, cursorTS, f.Cursor)
		}
	}

	query += " ORDER BY timestamp DESC, id DESC"
	if includeLimit && f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	return query, args, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(scanner rowScanner) (Event, error) {
	var evt Event
	var ts, detail, before, after string
	if err := scanner.Scan(&evt.ID, &ts, &evt.Type, &evt.ProbeID, &evt.Actor, &evt.Summary, &detail, &before, &after); err != nil {
		return Event{}, err
	}

	evt.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	if detail != "" && detail != "null" {
		_ = json.Unmarshal([]byte(detail), &evt.Detail)
	}
	if before != "" && before != "null" {
		_ = json.Unmarshal([]byte(before), &evt.Before)
	}
	if after != "" && after != "null" {
		_ = json.Unmarshal([]byte(after), &evt.After)
	}
	return evt, nil
}
