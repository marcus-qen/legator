package fleet

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/migration"
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
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	runner := migration.NewRunner("fleet", []migration.Migration{
		{
			Version:     1,
			Description: "initial fleet schema",
			Up: func(tx *sql.Tx) error {
				if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS probes (
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
					return err
				}
				_, _ = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_probes_status ON probes(status)`)
				_, _ = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_probes_last_seen ON probes(last_seen)`)
				return nil
			},
		},
		{
			Version:     2,
			Description: "add tenant_id to probes",
			Up: func(tx *sql.Tx) error {
				_, err := tx.Exec(`ALTER TABLE probes ADD COLUMN tenant_id TEXT NOT NULL DEFAULT ''`)
				if err != nil && strings.Contains(err.Error(), "duplicate column name") {
					return nil // idempotent
				}
				return err
			},
		},
		{
			Version:     3,
			Description: "add remote probe metadata",
			Up: func(tx *sql.Tx) error {
				if _, err := tx.Exec(`ALTER TABLE probes ADD COLUMN probe_type TEXT NOT NULL DEFAULT 'agent'`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
					return err
				}
				if _, err := tx.Exec(`ALTER TABLE probes ADD COLUMN remote TEXT`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
					return err
				}
				if _, err := tx.Exec(`ALTER TABLE probes ADD COLUMN remote_credentials TEXT`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
					return err
				}
				return nil
			},
		},
	})
	if err := runner.Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate fleet db: %w", err)
	}

	s := &Store{db: db, mgr: NewManager(logger)}

	if err := s.loadAll(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load probes: %w", err)
	}

	return s, nil
}

// Manager returns the underlying in-memory Manager.
func (s *Store) Manager() *Manager {
	return s.mgr
}

// ── Delegated reads (in-memory) ─────────────────────────────

func (s *Store) Get(id string) (*ProbeState, bool) { return s.mgr.Get(id) }

func (s *Store) FindByHostname(hostname string) (*ProbeState, bool) {
	if ps, ok := s.mgr.FindByHostname(hostname); ok {
		return ps, true
	}

	var id string
	err := s.db.QueryRow(`SELECT id
		FROM probes
		WHERE hostname = ?
		ORDER BY
			CASE lower(status)
				WHEN 'online' THEN 4
				WHEN 'degraded' THEN 3
				WHEN 'pending' THEN 2
				WHEN 'offline' THEN 1
				ELSE 0
			END DESC,
			last_seen DESC,
			registered DESC,
			id ASC
		LIMIT 1`, hostname).Scan(&id)
	if err != nil {
		return nil, false
	}

	return s.mgr.Get(id)
}

func (s *Store) List() []*ProbeState                             { return s.mgr.List() }
func (s *Store) ListRemote() []*ProbeState                       { return s.mgr.ListRemote() }
func (s *Store) Inventory(filter InventoryFilter) FleetInventory { return s.mgr.Inventory(filter) }
func (s *Store) Count() map[string]int                           { return s.mgr.Count() }
func (s *Store) ListByTag(tag string) []*ProbeState              { return s.mgr.ListByTag(tag) }
func (s *Store) TagCounts() map[string]int                       { return s.mgr.TagCounts() }
func (s *Store) ListByTenant(tenantID string) []*ProbeState      { return s.mgr.ListByTenant(tenantID) }

// ── Mutations (memory + disk) ───────────────────────────────

// Register adds or re-registers a probe.
func (s *Store) Register(id, hostname, os_, arch string) *ProbeState {
	ps := s.mgr.Register(id, hostname, os_, arch)
	_ = s.upsertProbe(ps)
	return ps
}

// RegisterRemote adds an SSH-backed remote probe.
func (s *Store) RegisterRemote(spec RemoteProbeRegistration) (*ProbeState, error) {
	ps, err := s.mgr.RegisterRemote(spec)
	if err != nil {
		return nil, err
	}
	_ = s.upsertProbe(ps)
	return ps, nil
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

// SetTenantID assigns a tenant to a probe, persisted to disk.
func (s *Store) SetTenantID(id, tenantID string) error {
	if err := s.mgr.SetTenantID(id, tenantID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE probes SET tenant_id = ? WHERE id = ?`, tenantID, id)
	return err
}

// SetStatus updates probe status and persists the change.
func (s *Store) SetStatus(id, status string) error {
	if err := s.mgr.SetStatus(id, status); err != nil {
		return err
	}
	ps, ok := s.mgr.Get(id)
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	return s.upsertProbe(ps)
}

// MarkOffline marks stale probes as offline and persists the change.
func (s *Store) MarkOffline(threshold time.Duration) {
	s.mgr.MarkOffline(threshold)
	for _, ps := range s.mgr.List() {
		if ps.Status == "offline" {
			_ = s.updateStatus(ps.ID, "offline")
		}
	}
}

// SetOnline marks a probe online and persists status + last_seen.
func (s *Store) SetOnline(id string) error {
	if err := s.mgr.SetOnline(id); err != nil {
		return err
	}
	ps, ok := s.mgr.Get(id)
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	return s.updateLastSeen(ps)
}

// Close shuts down the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// ── Internal persistence ────────────────────────────────────

func (s *Store) upsertProbe(ps *ProbeState) error {
	labels, _ := json.Marshal(ps.Labels)
	tags, _ := json.Marshal(ps.Tags)

	probeType := normalizeProbeType(ps.Type)
	if probeType == "" {
		probeType = ProbeTypeAgent
	}

	var inv []byte
	if ps.Inventory != nil {
		inv, _ = json.Marshal(ps.Inventory)
	}
	var remoteJSON []byte
	if ps.Remote != nil {
		remoteJSON, _ = json.Marshal(ps.Remote)
	}
	var credsJSON []byte
	if ps.RemoteCredentials != nil {
		// Manual marshal: RemoteProbeCredentials has json:"-" tags (API safety),
		// but we need to persist internally.
		cm := map[string]string{}
		if ps.RemoteCredentials.Password != "" {
			cm["password"] = ps.RemoteCredentials.Password
		}
		if ps.RemoteCredentials.PrivateKey != "" {
			cm["private_key"] = ps.RemoteCredentials.PrivateKey
		}
		credsJSON, _ = json.Marshal(cm)
	}

	_, err := s.db.Exec(`INSERT INTO probes (id, hostname, os, arch, status, probe_type, policy_level, api_key, registered, last_seen, labels, tags, inventory, tenant_id, remote, remote_credentials)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname           = excluded.hostname,
			os                 = excluded.os,
			arch               = excluded.arch,
			status             = excluded.status,
			probe_type         = excluded.probe_type,
			policy_level       = excluded.policy_level,
			api_key            = excluded.api_key,
			last_seen          = excluded.last_seen,
			labels             = excluded.labels,
			tags               = excluded.tags,
			inventory          = excluded.inventory,
			tenant_id          = excluded.tenant_id,
			remote             = excluded.remote,
			remote_credentials = excluded.remote_credentials`,
		ps.ID,
		ps.Hostname,
		ps.OS,
		ps.Arch,
		ps.Status,
		probeType,
		string(ps.PolicyLevel),
		ps.APIKey,
		ps.Registered.Format(time.RFC3339Nano),
		ps.LastSeen.Format(time.RFC3339Nano),
		string(labels),
		string(tags),
		nullableJSON(inv),
		ps.TenantID,
		nullableJSON(remoteJSON),
		nullableJSON(credsJSON),
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
	rows, err := s.db.Query(`SELECT id, hostname, os, arch, status, probe_type, policy_level, api_key, registered, last_seen, labels, tags, inventory, tenant_id, remote, remote_credentials FROM probes`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, hostname, os_, arch, status, probeType, policyLevel, apiKey string
			registered, lastSeen                                            string
			labelsJSON, tagsJSON                                            string
			invJSON                                                         sql.NullString
			tenantID                                                        string
			remoteJSON                                                      sql.NullString
			credsJSON                                                       sql.NullString
		)
		if err := rows.Scan(&id, &hostname, &os_, &arch, &status, &probeType, &policyLevel, &apiKey, &registered, &lastSeen, &labelsJSON, &tagsJSON, &invJSON, &tenantID, &remoteJSON, &credsJSON); err != nil {
			continue
		}

		ps := &ProbeState{
			ID:          id,
			Hostname:    hostname,
			OS:          os_,
			Arch:        arch,
			Status:      status,
			Type:        normalizeProbeType(probeType),
			PolicyLevel: protocol.CapabilityLevel(policyLevel),
			APIKey:      apiKey,
			TenantID:    tenantID,
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
		if remoteJSON.Valid && strings.TrimSpace(remoteJSON.String) != "" {
			var remote RemoteProbeConfig
			if err := json.Unmarshal([]byte(remoteJSON.String), &remote); err == nil {
				ps.Remote = &remote
			}
		}
		if credsJSON.Valid && strings.TrimSpace(credsJSON.String) != "" {
			var cm map[string]string
			if err := json.Unmarshal([]byte(credsJSON.String), &cm); err == nil {
				creds := RemoteProbeCredentials{
					Password:   cm["password"],
					PrivateKey: cm["private_key"],
				}
				ps.RemoteCredentials = &creds
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

// Delete removes a probe from the store and in-memory cache.
func (s *Store) Delete(id string) error {
	if err := s.mgr.Delete(id); err != nil {
		return err
	}
	_, _ = s.db.Exec("DELETE FROM probes WHERE id = ?", id)
	return nil
}

// CleanupOffline removes probes offline longer than the threshold.
func (s *Store) CleanupOffline(olderThan time.Duration) []string {
	removed := s.mgr.CleanupOffline(olderThan)
	for _, id := range removed {
		_, _ = s.db.Exec("DELETE FROM probes WHERE id = ?", id)
	}
	return removed
}
