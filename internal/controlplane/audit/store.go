package audit

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
	_ "modernc.org/sqlite"
)

// StoreOptions controls optional audit store features.
type StoreOptions struct {
	ChainMode bool
	ChainKey  string // hex-encoded HMAC key
}

// VerifyResult reports the outcome of a full chain verification pass.
type VerifyResult struct {
	Valid          bool
	EntriesChecked int
	FirstInvalidAt *string
}

// Store provides persistent audit log storage backed by SQLite.
// It wraps the in-memory Log and syncs events to disk.
type Store struct {
	db          *sql.DB
	log         *Log // in-memory cache for fast queries
	memoryLimit int
	mu          sync.RWMutex

	writeMu       sync.Mutex
	chainMode     bool
	chainKey      []byte
	lastEntryHash string
}

func migrateAuditStore(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE audit_events ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !isDuplicateColumnError(err) {
			return err
		}
	}
	if _, err := db.Exec(`ALTER TABLE audit_events ADD COLUMN prev_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		if !isDuplicateColumnError(err) {
			return err
		}
	}
	if _, err := db.Exec(`ALTER TABLE audit_events ADD COLUMN entry_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		if !isDuplicateColumnError(err) {
			return err
		}
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_workspace ON audit_events(workspace_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_entry_hash ON audit_events(entry_hash)`)
	return nil
}

func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate column name")
}

// NewStore opens (or creates) a SQLite-backed audit store.
func NewStore(dbPath string, memoryLimit int) (*Store, error) {
	return NewStoreWithOptions(dbPath, memoryLimit, StoreOptions{})
}

// NewStoreWithOptions opens a SQLite-backed audit store with optional chain signing.
func NewStoreWithOptions(dbPath string, memoryLimit int, opts StoreOptions) (*Store, error) {
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
		id           TEXT PRIMARY KEY,
		timestamp    TEXT NOT NULL,
		type         TEXT NOT NULL,
		probe_id     TEXT,
		workspace_id TEXT NOT NULL DEFAULT '',
		actor        TEXT,
		summary      TEXT,
		detail       TEXT,
		before_val   TEXT,
		after_val    TEXT,
		prev_hash    TEXT NOT NULL DEFAULT '',
		entry_hash   TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		db.Close()
		return nil, err
	}

	if err := migration.EnsureVersion(db, 1); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure schema version: %w", err)
	}
	if err := migrateAuditStore(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate audit store: %w", err)
	}

	// Index for common queries
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_workspace ON audit_events(workspace_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_probe ON audit_events(probe_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_type ON audit_events(type)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_events(timestamp)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_entry_hash ON audit_events(entry_hash)`)

	s := &Store{
		db:          db,
		log:         NewLog(memoryLimit),
		memoryLimit: memoryLimit,
		chainMode:   opts.ChainMode,
	}

	if s.chainMode {
		rawKey := strings.TrimSpace(opts.ChainKey)
		if rawKey == "" {
			generated, err := GenerateChainKeyHex()
			if err != nil {
				_ = db.Close()
				return nil, err
			}
			rawKey = generated
		}
		decoded, err := DecodeChainKey(rawKey)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		s.chainKey = decoded
	}

	// Load recent events into memory cache
	if err := s.loadRecent(memoryLimit); err != nil {
		_ = err // Non-fatal — store still works
	}

	if s.chainMode {
		h, err := s.latestPersistedEntryHash()
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("load last entry hash: %w", err)
		}
		s.lastEntryHash = h
	}

	return s, nil
}

// ChainModeEnabled reports whether hash-chain signing is enabled for this store.
func (s *Store) ChainModeEnabled() bool {
	return s != nil && s.chainMode
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

	if s.chainMode {
		s.writeMu.Lock()
		prevHash := s.lastEntryHash
		if prevHash == "" {
			prevHash = GenesisHash
		}
		evt.PrevHash = prevHash

		entryHash, hashErr := ComputeEntryHash(prevHash, evt, s.chainKey)
		if hashErr == nil {
			evt.EntryHash = entryHash
			if inserted, err := s.persist(evt); err == nil && inserted {
				s.lastEntryHash = entryHash
			}
		} else {
			_, _ = s.persist(evt)
		}
		s.writeMu.Unlock()
	} else {
		_, _ = s.persist(evt)
	}

	s.mu.RLock()
	s.log.Record(evt)
	s.mu.RUnlock()
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
	if s.chainMode {
		if err := cw.Write([]string{"id", "timestamp", "type", "probe_id", "actor", "summary", "prev_hash", "entry_hash"}); err != nil {
			return err
		}
	} else {
		if err := cw.Write([]string{"id", "timestamp", "type", "probe_id", "actor", "summary"}); err != nil {
			return err
		}
	}

	for rows.Next() {
		if s.chainMode {
			var id, ts, typ, probeID, actor, summary, prevHash, entryHash string
			if err := rows.Scan(&id, &ts, &typ, &probeID, &actor, &summary, &prevHash, &entryHash); err != nil {
				continue
			}
			if err := cw.Write([]string{id, ts, typ, probeID, actor, summary, prevHash, entryHash}); err != nil {
				return err
			}
			continue
		}

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

// VerifyChain verifies the persisted signed chain and reports the first invalid row, if any.
//
// Unsigned rows are permitted as a prefix (before chain mode was enabled). Once a signed
// row appears, all subsequent rows must be signed and contiguous.
func (s *Store) VerifyChain(ctx context.Context) (VerifyResult, error) {
	if !s.chainMode {
		return VerifyResult{Valid: true, EntriesChecked: 0, FirstInvalidAt: nil}, nil
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, timestamp, type, probe_id, workspace_id, actor, summary, detail, before_val, after_val, prev_hash, entry_hash
		FROM audit_events ORDER BY timestamp ASC, id ASC`)
	if err != nil {
		return VerifyResult{}, err
	}
	defer rows.Close()

	result := VerifyResult{Valid: true}
	signedStarted := false
	expectedPrev := GenesisHash

	for rows.Next() {
		var evt Event
		var ts, detail, before, after string
		if err := rows.Scan(&evt.ID, &ts, &evt.Type, &evt.ProbeID, &evt.WorkspaceID, &evt.Actor, &evt.Summary, &detail, &before, &after, &evt.PrevHash, &evt.EntryHash); err != nil {
			return VerifyResult{}, err
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

		hasChainData := evt.PrevHash != "" || evt.EntryHash != ""
		if !hasChainData {
			if signedStarted {
				result.Valid = false
				result.FirstInvalidAt = stringPtr(ts)
				return result, nil
			}
			continue
		}
		if evt.PrevHash == "" || evt.EntryHash == "" {
			result.Valid = false
			result.FirstInvalidAt = stringPtr(ts)
			return result, nil
		}

		if !signedStarted {
			signedStarted = true
			expectedPrev = GenesisHash
		}

		if evt.PrevHash != expectedPrev {
			result.Valid = false
			result.FirstInvalidAt = stringPtr(ts)
			return result, nil
		}

		expectedHash, err := ComputeEntryHash(evt.PrevHash, evt, s.chainKey)
		if err != nil {
			return VerifyResult{}, err
		}
		result.EntriesChecked++
		if evt.EntryHash != expectedHash {
			result.Valid = false
			result.FirstInvalidAt = stringPtr(ts)
			return result, nil
		}
		expectedPrev = evt.EntryHash
	}

	if err := rows.Err(); err != nil {
		return VerifyResult{}, err
	}

	return result, nil
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
		if s.chainMode {
			h, err := s.latestPersistedEntryHash()
			if err != nil {
				return deleted, err
			}
			s.writeMu.Lock()
			s.lastEntryHash = h
			s.writeMu.Unlock()
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

func (s *Store) persist(evt Event) (bool, error) {
	detail, _ := json.Marshal(evt.Detail)
	before, _ := json.Marshal(evt.Before)
	after, _ := json.Marshal(evt.After)

	res, err := s.db.Exec(`INSERT OR IGNORE INTO audit_events (
		id, timestamp, type, probe_id, workspace_id, actor, summary, detail, before_val, after_val, prev_hash, entry_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		evt.ID,
		evt.Timestamp.Format(time.RFC3339Nano),
		string(evt.Type),
		evt.ProbeID,
		strings.TrimSpace(evt.WorkspaceID),
		evt.Actor,
		evt.Summary,
		string(detail),
		string(before),
		string(after),
		evt.PrevHash,
		evt.EntryHash,
	)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, nil
	}
	return rows > 0, nil
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

func (s *Store) latestPersistedEntryHash() (string, error) {
	var last string
	err := s.db.QueryRow(`SELECT entry_hash FROM audit_events WHERE entry_hash <> '' ORDER BY timestamp DESC, id DESC LIMIT 1`).Scan(&last)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return last, nil
}

func (s *Store) buildPersistedQuery(f Filter, includeLimit bool, csvMode bool) (string, []any, error) {
	query := "SELECT id, timestamp, type, probe_id, workspace_id, actor, summary, detail, before_val, after_val, prev_hash, entry_hash FROM audit_events WHERE 1=1"
	if csvMode {
		if s.chainMode {
			query = "SELECT id, timestamp, type, probe_id, actor, summary, prev_hash, entry_hash FROM audit_events WHERE 1=1"
		} else {
			query = "SELECT id, timestamp, type, probe_id, actor, summary FROM audit_events WHERE 1=1"
		}
	}
	var args []any

	if f.WorkspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, f.WorkspaceID)
	}

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
	if err := scanner.Scan(&evt.ID, &ts, &evt.Type, &evt.ProbeID, &evt.WorkspaceID, &evt.Actor, &evt.Summary, &detail, &before, &after, &evt.PrevHash, &evt.EntryHash); err != nil {
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

func stringPtr(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	vv := v
	return &vv
}
