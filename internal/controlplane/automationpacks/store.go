package automationpacks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound      = errors.New("automation pack not found")
	ErrAlreadyExists = errors.New("automation pack definition already exists")
)

// Store persists automation pack workflow definitions.
type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open automation pack db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS automation_packs (
		id              TEXT NOT NULL,
		version         TEXT NOT NULL,
		name            TEXT NOT NULL,
		description     TEXT NOT NULL DEFAULT '',
		definition_json TEXT NOT NULL,
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		PRIMARY KEY (id, version)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create automation_packs: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_automation_packs_updated ON automation_packs(updated_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_automation_packs_id ON automation_packs(id)`)

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) CreateDefinition(def Definition) (*Definition, error) {
	if err := ValidateDefinition(&def); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now

	payload, err := json.Marshal(def)
	if err != nil {
		return nil, fmt.Errorf("marshal definition: %w", err)
	}

	_, err = s.db.Exec(`INSERT INTO automation_packs
		(id, version, name, description, definition_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		def.Metadata.ID,
		def.Metadata.Version,
		def.Metadata.Name,
		def.Metadata.Description,
		string(payload),
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("insert automation pack: %w", err)
	}

	return s.GetDefinition(def.Metadata.ID, def.Metadata.Version)
}

func (s *Store) ListDefinitions() ([]DefinitionSummary, error) {
	rows, err := s.db.Query(`SELECT id, version, name, description, definition_json, created_at, updated_at
		FROM automation_packs
		ORDER BY id ASC, version DESC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DefinitionSummary, 0)
	for rows.Next() {
		var (
			summary                                DefinitionSummary
			definitionRaw, createdAtRaw, updatedAtRaw string
		)

		if err := rows.Scan(
			&summary.Metadata.ID,
			&summary.Metadata.Version,
			&summary.Metadata.Name,
			&summary.Metadata.Description,
			&definitionRaw,
			&createdAtRaw,
			&updatedAtRaw,
		); err != nil {
			continue
		}

		var definition Definition
		if err := json.Unmarshal([]byte(definitionRaw), &definition); err == nil {
			summary.InputCount = len(definition.Inputs)
			summary.StepCount = len(definition.Steps)
		}

		summary.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
		summary.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtRaw)
		out = append(out, summary)
	}

	return out, rows.Err()
}

func (s *Store) GetDefinition(id, version string) (*Definition, error) {
	id = strings.TrimSpace(strings.ToLower(id))
	version = strings.TrimSpace(version)
	if id == "" {
		return nil, ErrNotFound
	}

	var row *sql.Row
	if version == "" {
		row = s.db.QueryRow(`SELECT definition_json
			FROM automation_packs
			WHERE id = ?
			ORDER BY updated_at DESC
			LIMIT 1`, id)
	} else {
		row = s.db.QueryRow(`SELECT definition_json
			FROM automation_packs
			WHERE id = ? AND version = ?`, id, version)
	}

	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	var definition Definition
	if err := json.Unmarshal([]byte(raw), &definition); err != nil {
		return nil, fmt.Errorf("decode definition: %w", err)
	}
	return &definition, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
