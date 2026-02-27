package modeldock

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Store persists model profiles and token usage.
type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open model dock db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_profiles (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		provider   TEXT NOT NULL,
		base_url   TEXT NOT NULL,
		model      TEXT NOT NULL,
		api_key    TEXT NOT NULL,
		is_active  INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create model_profiles: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_usage (
		id                TEXT PRIMARY KEY,
		ts                TEXT NOT NULL,
		profile_id        TEXT NOT NULL,
		feature           TEXT NOT NULL,
		prompt_tokens     INTEGER NOT NULL,
		completion_tokens INTEGER NOT NULL,
		total_tokens      INTEGER NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create model_usage: %w", err)
	}

	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_model_profiles_single_active ON model_profiles(is_active) WHERE is_active = 1`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_model_profiles_updated_at ON model_profiles(updated_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_model_usage_ts ON model_usage(ts)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_model_usage_profile_feature ON model_usage(profile_id, feature)`)

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) ListProfiles() ([]Profile, error) {
	rows, err := s.db.Query(`SELECT id, name, provider, base_url, model, api_key, is_active, created_at, updated_at
		FROM model_profiles
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Profile, 0)
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			continue
		}
		profile.Source = SourceDB
		out = append(out, *profile)
	}

	return out, rows.Err()
}

func (s *Store) CountProfiles() (int, error) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM model_profiles").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) HasProfiles() (bool, error) {
	count, err := s.CountProfiles()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) CreateProfile(profile Profile) (*Profile, error) {
	now := time.Now().UTC()
	if profile.ID == "" {
		profile.ID = uuid.NewString()
	}
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Provider = strings.TrimSpace(profile.Provider)
	profile.BaseURL = strings.TrimSpace(profile.BaseURL)
	profile.Model = strings.TrimSpace(profile.Model)
	profile.CreatedAt = now
	profile.UpdatedAt = now

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if profile.IsActive {
		if _, err := tx.Exec(`UPDATE model_profiles SET is_active = 0, updated_at = ? WHERE is_active = 1`, now.Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}

	active := 0
	if profile.IsActive {
		active = 1
	}

	if _, err := tx.Exec(`INSERT INTO model_profiles (id, name, provider, base_url, model, api_key, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.ID,
		profile.Name,
		profile.Provider,
		profile.BaseURL,
		profile.Model,
		profile.APIKey,
		active,
		profile.CreatedAt.Format(time.RFC3339Nano),
		profile.UpdatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("insert profile: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	created, err := s.GetProfile(profile.ID)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Store) GetProfile(id string) (*Profile, error) {
	row := s.db.QueryRow(`SELECT id, name, provider, base_url, model, api_key, is_active, created_at, updated_at
		FROM model_profiles
		WHERE id = ?`, id)
	profile, err := scanProfile(row)
	if err != nil {
		return nil, err
	}
	profile.Source = SourceDB
	return profile, nil
}

func (s *Store) GetActiveProfile() (*Profile, error) {
	row := s.db.QueryRow(`SELECT id, name, provider, base_url, model, api_key, is_active, created_at, updated_at
		FROM model_profiles
		WHERE is_active = 1
		LIMIT 1`)
	profile, err := scanProfile(row)
	if err != nil {
		return nil, err
	}
	profile.Source = SourceDB
	return profile, nil
}

func (s *Store) HasActiveExcluding(id string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM model_profiles WHERE is_active = 1 AND id != ?`, id).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) UpdateProfile(id string, profile Profile) (*Profile, error) {
	existing, err := s.GetProfile(id)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	apiKey := existing.APIKey
	if strings.TrimSpace(profile.APIKey) != "" {
		apiKey = strings.TrimSpace(profile.APIKey)
	}

	result, err := s.db.Exec(`UPDATE model_profiles
		SET name = ?, provider = ?, base_url = ?, model = ?, api_key = ?, updated_at = ?
		WHERE id = ?`,
		strings.TrimSpace(profile.Name),
		strings.TrimSpace(profile.Provider),
		strings.TrimSpace(profile.BaseURL),
		strings.TrimSpace(profile.Model),
		apiKey,
		now.Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("update profile: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	if profile.IsActive {
		if _, err := s.ActivateProfile(id); err != nil {
			return nil, err
		}
	}

	return s.GetProfile(id)
}

func (s *Store) ActivateProfile(id string) (*Profile, error) {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE model_profiles SET is_active = 0, updated_at = ? WHERE is_active = 1`, now.Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}

	result, err := tx.Exec(`UPDATE model_profiles SET is_active = 1, updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetProfile(id)
}

func (s *Store) DeleteProfile(id string) error {
	result, err := s.db.Exec(`DELETE FROM model_profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RecordUsage(record UsageRecord) error {
	if strings.TrimSpace(record.ProfileID) == "" {
		record.ProfileID = EnvProfileID
	}
	if !IsValidFeature(record.Feature) {
		return fmt.Errorf("invalid feature: %s", record.Feature)
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.TS.IsZero() {
		record.TS = time.Now().UTC()
	}
	if record.TotalTokens == 0 {
		record.TotalTokens = record.PromptTokens + record.CompletionTokens
	}
	_, err := s.db.Exec(`INSERT INTO model_usage (id, ts, profile_id, feature, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.TS.Format(time.RFC3339Nano),
		record.ProfileID,
		record.Feature,
		record.PromptTokens,
		record.CompletionTokens,
		record.TotalTokens,
	)
	return err
}

func (s *Store) AggregateUsage(window time.Duration) ([]UsageAggregate, UsageAggregate, time.Time, error) {
	if window <= 0 {
		window = 24 * time.Hour
	}
	since := time.Now().UTC().Add(-window)

	rows, err := s.db.Query(`SELECT
		u.profile_id,
		COALESCE(p.name, ''),
		u.feature,
		COUNT(*) AS requests,
		SUM(u.prompt_tokens) AS prompt_tokens,
		SUM(u.completion_tokens) AS completion_tokens,
		SUM(u.total_tokens) AS total_tokens
		FROM model_usage u
		LEFT JOIN model_profiles p ON p.id = u.profile_id
		WHERE u.ts >= ?
		GROUP BY u.profile_id, p.name, u.feature
		ORDER BY total_tokens DESC`, since.Format(time.RFC3339Nano))
	if err != nil {
		return nil, UsageAggregate{}, since, err
	}
	defer rows.Close()

	items := make([]UsageAggregate, 0)
	totals := UsageAggregate{Feature: "all"}
	for rows.Next() {
		var item UsageAggregate
		if err := rows.Scan(
			&item.ProfileID,
			&item.ProfileName,
			&item.Feature,
			&item.Requests,
			&item.PromptTokens,
			&item.CompletionTokens,
			&item.TotalTokens,
		); err != nil {
			continue
		}
		items = append(items, item)
		totals.Requests += item.Requests
		totals.PromptTokens += item.PromptTokens
		totals.CompletionTokens += item.CompletionTokens
		totals.TotalTokens += item.TotalTokens
	}

	return items, totals, since, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProfile(row scanner) (*Profile, error) {
	var (
		profile              Profile
		active               int
		createdAt, updatedAt string
	)
	if err := row.Scan(
		&profile.ID,
		&profile.Name,
		&profile.Provider,
		&profile.BaseURL,
		&profile.Model,
		&profile.APIKey,
		&active,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	profile.IsActive = active == 1
	profile.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &profile, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
