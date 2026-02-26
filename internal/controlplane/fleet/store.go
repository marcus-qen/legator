package fleet

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

// Store provides persistent fleet management backed by SQLite.
// Reads are served from the in-memory Manager for speed; mutations are
// written to both memory and disk.
type Store struct {
	db  *sql.DB
	mgr *Manager
}

// NewStore opens (or creates) a SQLite-backed fleet store.
func NewStore(dbPath string, logger *zap.Logger) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open fleet db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS probes (
		id           TEXT PRIMARY KEY,
		hostname     TEXT NOT NULL DEFAULT '',
		os           TEXT NOT NULL DEFAULT '',
		arch         TEXT NOT NULL DEFAULT '',
		status       TEXT NOT NULL DEFAULT 'pending',
		policy_level TEXT NOT NULL DEFAULT 'observe',
		api_key      TEXT NOT NULL DEFAULT '',
		registered   TEXT NOT NULL,
		last_seen    TEXT NOT NULL,
		labels       TEXT NOT NULL DEFAULT '{}',
		tags         TEXT NOT NULL DEFAULT '[]',
		inventory    TEXT
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create probes table: %w", err)
	}

	if _, err := db.Exec(`ALTER TABLE probes ADD COLUMN api_key TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name: api_key") {
			db.Close()
			return nil, fmt.Errorf("add api_key column: %w", err)
		}
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_probes_status ON probes(status)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_probes_last_seen ON probes(last_seen)`)

	s := &Store{db: db, mgr: NewManager(logger)}

	if err := s.loadAll(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load probes: %w", err)
	}

	return s, nil
}

// Manager returns the underlying in-memory Manager (for read-only callers
// that already depend on the Manager type).
func (s *Store) Manager() *Manager {
	return s.mgr
}

// ── Delegated reads (in-memory) ─────────────────────────────

func (s *Store) Get(id string) (*ProbeState, bool)  { return s.mgr.Get(id) }
func (s *Store) List() []*ProbeState                { return s.mgr.List() }
func (s *Store) Count() map[string]int              { return s.mgr.Count() }
func (s *Store) ListByTag(tag string) []*ProbeState { return s.mgr.ListByTag(tag) }
func (s *Store) TagCounts() map[string]int          { return s.mgr.TagCounts() }

// ── Mutations (memory + disk) ───────────────────────────────

// Register adds or re-registers a probe.
func (s *Store) Register(id, hostname, os_, arch string) *ProbeState {
	ps := s.mgr.Register(id, hostname, os_, arch)
	_ = s.upsertProbe(ps)
	return ps
}

// Heartbeat updates the probe last-seen time.
func (s *Store) Heartbeat(id string, hb *protocol.HeartbeatPayload) error {
	if err := s.mgr.Heartbeat(id, hb); err != nil {
		return err
	}
	ps, ok := s.mgr.Get(id)
	if ok {
		_ = s.updateLastSeen(ps)
	}
	return nil
}

// UpdateInventory stores a probe inventory.
func (s *Store) UpdateInventory(id string, inv *protocol.InventoryPayload) error {
	if err := s.mgr.UpdateInventory(id, inv); err != nil {
		return err
	}
	ps, ok := s.mgr.Get(id)
	if ok {
		_ = s.upsertProbe(ps)
	}
	return nil
}

// SetPolicy updates a probe capability level.
func (s *Store) SetPolicy(id string, level protocol.CapabilityLevel) error {
	if err := s.mgr.SetPolicy(id, level); err != nil {
		return err
	}
	ps, ok := s.mgr.Get(id)
	if ok {
		_ = s.upsertProbe(ps)
	}
	return nil
}

// SetAPIKey updates a probe API key.
func (s *Store) SetAPIKey(id, apiKey string) error {
	if err := s.mgr.SetAPIKey(id, apiKey); err != nil {
		return err
	}
	ps, ok := s.mgr.Get(id)
	if ok {
		_ = s.upsertProbe(ps)
	}
	return nil
}

// SetTags replaces the probe tags.
func (s *Store) SetTags(id string, tags []string) error {
	if err := s.mgr.SetTags(id, tags); err != nil {
		return err
	}
	ps, ok := s.mgr.Get(id)
	if ok {
		_ = s.upsertProbe(ps)
	}
	return nil
}

// MarkOffline marks stale probes as offline and persists the change.
func (s *Store) MarkOffline(threshold time.Duration) {
	s.mgr.MarkOffline(threshold)

	// Persist status changes
	for _, ps := range s.mgr.List() {
		if ps.Status == "offline" {
			_ = s.updateStatus(ps.ID, "offline")
		}
	}
}

// Close shuts down the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// ── Internal persistence ────────────────────────────────────

func (s *Store) upsertProbe(ps *ProbeState) error {
	labels, _ := json.Marshal(ps.Labels)
	tags, _ := json.Marshal(ps.Tags)
	var inv []byte
	if ps.Inventory != nil {
		inv, _ = json.Marshal(ps.Inventory)
	}

	_, err := s.db.Exec(`INSERT INTO probes (id, hostname, os, arch, status, policy_level, api_key, registered, last_seen, labels, tags, inventory)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname     = excluded.hostname,
			os           = excluded.os,
			arch         = excluded.arch,
			status       = excluded.status,
			policy_level = excluded.policy_level,
			api_key      = excluded.api_key,
			last_seen    = excluded.last_seen,
			labels       = excluded.labels,
			tags         = excluded.tags,
			inventory    = excluded.inventory`,
		ps.ID,
		ps.Hostname,
		ps.OS,
		ps.Arch,
		ps.Status,
		string(ps.PolicyLevel),
		ps.APIKey,
		ps.Registered.Format(time.RFC3339Nano),
		ps.LastSeen.Format(time.RFC3339Nano),
		string(labels),
		string(tags),
		nullableJSON(inv),
	)
	return err
}

func (s *Store) updateLastSeen(ps *ProbeState) error {
	_, err := s.db.Exec(`UPDATE probes SET last_seen = ?, status = ? WHERE id = ?`,
		ps.LastSeen.Format(time.RFC3339Nano), ps.Status, ps.ID)
	return err
}

func (s *Store) updateStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE probes SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) loadAll() error {
	rows, err := s.db.Query(`SELECT id, hostname, os, arch, status, policy_level, api_key, registered, last_seen, labels, tags, inventory FROM probes`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, hostname, os_, arch, status, policyLevel, apiKey string
			registered, lastSeen                                 string
			labelsJSON, tagsJSON                                 string
			invJSON                                              sql.NullString
		)
		if err := rows.Scan(&id, &hostname, &os_, &arch, &status, &policyLevel, &apiKey, &registered, &lastSeen, &labelsJSON, &tagsJSON, &invJSON); err != nil {
			continue
		}

		ps := &ProbeState{
			ID:          id,
			Hostname:    hostname,
			OS:          os_,
			Arch:        arch,
			Status:      status,
			PolicyLevel: protocol.CapabilityLevel(policyLevel),
			APIKey:      apiKey,
			Labels:      map[string]string{},
			Tags:        []string{},
		}
		ps.Registered, _ = time.Parse(time.RFC3339Nano, registered)
		ps.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)

		if labelsJSON != "" && labelsJSON != "{}" {
			_ = json.Unmarshal([]byte(labelsJSON), &ps.Labels)
		}
		if tagsJSON != "" && tagsJSON != "[]" {
			_ = json.Unmarshal([]byte(tagsJSON), &ps.Tags)
		}
		if invJSON.Valid && invJSON.String != "" {
			var inv protocol.InventoryPayload
			if err := json.Unmarshal([]byte(invJSON.String), &inv); err == nil {
				ps.Inventory = &inv
			}
		}

		s.mgr.mu.Lock()
		s.mgr.probes[id] = ps
		s.mgr.mu.Unlock()
	}

	return rows.Err()
}

func nullableJSON(data []byte) sql.NullString {
	if data == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(data), Valid: true}
}
