package webhook

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store provides persistent webhook storage backed by SQLite.
type Store struct {
	db       *sql.DB
	notifier *Notifier
}

// NewStore opens (or creates) a SQLite-backed webhook store.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open webhook db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS webhooks (
		id      TEXT PRIMARY KEY,
		url     TEXT NOT NULL,
		events  TEXT NOT NULL DEFAULT '[]',
		secret  TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1
	)`); err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{db: db, notifier: NewNotifier()}

	if err := s.loadAll(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

// Notifier returns the underlying notifier (for dispatch and handlers).
func (s *Store) Notifier() *Notifier {
	return s.notifier
}

// Register adds a webhook and persists it.
func (s *Store) Register(cfg WebhookConfig) {
	s.notifier.Register(cfg)
	_ = s.persist(cfg)
}

// Remove deletes a webhook and removes from disk.
func (s *Store) Remove(id string) {
	s.notifier.Remove(id)
	s.db.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
}

// List returns all webhooks.
func (s *Store) List() []WebhookConfig {
	return s.notifier.List()
}

// Notify dispatches to matching webhooks.
func (s *Store) Notify(event, probeID, summary string, detail any) {
	s.notifier.Notify(event, probeID, summary, detail)
}

// Close shuts down the store.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) persist(cfg WebhookConfig) error {
	eventsJSON, _ := json.Marshal(cfg.Events)
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}

	_, err := s.db.Exec(`INSERT INTO webhooks (id, url, events, secret, enabled)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			url = excluded.url,
			events = excluded.events,
			secret = excluded.secret,
			enabled = excluded.enabled`,
		cfg.ID, cfg.URL, string(eventsJSON), cfg.Secret, enabled)
	return err
}

func (s *Store) loadAll() error {
	rows, err := s.db.Query(`SELECT id, url, events, secret, enabled FROM webhooks`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, url, eventsJSON, secret string
			enabled                     int
		)
		if err := rows.Scan(&id, &url, &eventsJSON, &secret, &enabled); err != nil {
			continue
		}

		var events []string
		_ = json.Unmarshal([]byte(eventsJSON), &events)

		s.notifier.Register(WebhookConfig{
			ID:      id,
			URL:     url,
			Events:  events,
			Secret:  secret,
			Enabled: enabled == 1,
		})
	}

	return rows.Err()
}
