package policy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	_ "modernc.org/sqlite"
)

// PersistentStore wraps Store with SQLite persistence.
type PersistentStore struct {
	*Store
	db *sql.DB
}

// NewPersistentStore opens (or creates) a SQLite-backed policy store.
func NewPersistentStore(dbPath string) (*PersistentStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open policy db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS policy_templates (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		level       TEXT NOT NULL,
		allowed     TEXT NOT NULL DEFAULT '[]',
		blocked     TEXT NOT NULL DEFAULT '[]',
		paths       TEXT NOT NULL DEFAULT '[]',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}

	base := NewStore()
	ps := &PersistentStore{Store: base, db: db}

	if err := ps.loadFromDB(); err != nil {
		db.Close()
		return nil, err
	}

	return ps, nil
}

// Create adds a template and persists it.
func (ps *PersistentStore) Create(name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string) *Template {
	t := ps.Store.Create(name, description, level, allowed, blocked, paths)
	_ = ps.persist(t)
	return t
}

// Update modifies a template and persists it.
func (ps *PersistentStore) Update(id string, name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string) (*Template, error) {
	t, err := ps.Store.Update(id, name, description, level, allowed, blocked, paths)
	if err != nil {
		return nil, err
	}
	_ = ps.persist(t)
	return t, nil
}

// Delete removes a template from both memory and disk.
func (ps *PersistentStore) Delete(id string) error {
	if err := ps.Store.Delete(id); err != nil {
		return err
	}
	_, _ = ps.db.Exec(`DELETE FROM policy_templates WHERE id = ?`, id)
	return nil
}

// Close shuts down the database.
func (ps *PersistentStore) Close() error {
	return ps.db.Close()
}

func (ps *PersistentStore) persist(t *Template) error {
	allowedJSON, _ := json.Marshal(t.Allowed)
	blockedJSON, _ := json.Marshal(t.Blocked)
	pathsJSON, _ := json.Marshal(t.Paths)

	_, err := ps.db.Exec(`INSERT INTO policy_templates (id, name, description, level, allowed, blocked, paths, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			level = excluded.level,
			allowed = excluded.allowed,
			blocked = excluded.blocked,
			paths = excluded.paths,
			updated_at = excluded.updated_at`,
		t.ID, t.Name, t.Description, string(t.Level),
		string(allowedJSON), string(blockedJSON), string(pathsJSON),
		t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339))
	return err
}

func (ps *PersistentStore) loadFromDB() error {
	rows, err := ps.db.Query(`SELECT id, name, description, level, allowed, blocked, paths, created_at, updated_at FROM policy_templates`)
	if err != nil {
		return err
	}
	defer rows.Close()

	ps.Store.mu.Lock()
	defer ps.Store.mu.Unlock()

	for rows.Next() {
		var (
			id, name, desc, level     string
			allowedJSON, blockedJSON  string
			pathsJSON                 string
			createdStr, updatedStr    string
		)
		if err := rows.Scan(&id, &name, &desc, &level, &allowedJSON, &blockedJSON, &pathsJSON, &createdStr, &updatedStr); err != nil {
			continue
		}

		var allowed, blocked, paths []string
		_ = json.Unmarshal([]byte(allowedJSON), &allowed)
		_ = json.Unmarshal([]byte(blockedJSON), &blocked)
		_ = json.Unmarshal([]byte(pathsJSON), &paths)

		created, _ := time.Parse(time.RFC3339, createdStr)
		updated, _ := time.Parse(time.RFC3339, updatedStr)

		ps.Store.templates[id] = &Template{
			ID:          id,
			Name:        name,
			Description: desc,
			Level:       protocol.CapabilityLevel(level),
			Allowed:     allowed,
			Blocked:     blocked,
			Paths:       paths,
			CreatedAt:   created,
			UpdatedAt:   updated,
		}
	}

	return rows.Err()
}
