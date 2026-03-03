package tokenbroker

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
	ErrStoreTokenNotFound = errors.New("token not found")
	ErrStoreTokenExists   = errors.New("token already exists")
)

// TokenState is the persisted server-side token record.
type TokenState struct {
	Token      string
	Scope      []string
	Audience   string
	RunnerID   string
	JobID      string
	Issuer     string
	SessionID  string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

// Store persists token state in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a token broker SQLite store.
func NewStore(dbPath string) (*Store, error) {
	path := strings.TrimSpace(dbPath)
	if path == "" {
		path = ":memory:"
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open token broker db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS token_broker_tokens (
		token TEXT PRIMARY KEY,
		scope_json TEXT NOT NULL,
		audience TEXT NOT NULL,
		runner_id TEXT NOT NULL,
		job_id TEXT NOT NULL,
		issuer TEXT NOT NULL,
		session_id TEXT NOT NULL,
		issued_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		consumed_at TEXT
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create token broker table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_token_broker_expires_at ON token_broker_tokens(expires_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_token_broker_runner_id ON token_broker_tokens(runner_id)`)

	return &Store{db: db}, nil
}

// Insert stores a new token state.
func (s *Store) Insert(state *TokenState) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("token broker store unavailable")
	}
	if state == nil {
		return fmt.Errorf("token state is required")
	}

	scopeJSON, err := json.Marshal(state.Scope)
	if err != nil {
		return fmt.Errorf("encode scope: %w", err)
	}

	_, err = s.db.Exec(`INSERT INTO token_broker_tokens (
		token, scope_json, audience, runner_id, job_id, issuer, session_id, issued_at, expires_at, consumed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		state.Token,
		string(scopeJSON),
		state.Audience,
		state.RunnerID,
		state.JobID,
		state.Issuer,
		state.SessionID,
		state.IssuedAt.UTC().Format(time.RFC3339Nano),
		state.ExpiresAt.UTC().Format(time.RFC3339Nano),
		nil,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrStoreTokenExists
		}
		return fmt.Errorf("insert token: %w", err)
	}
	return nil
}

// Get returns token state by token value.
func (s *Store) Get(token string) (*TokenState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("token broker store unavailable")
	}

	var (
		state      TokenState
		scopeJSON  string
		issuedAt   string
		expiresAt  string
		consumedAt sql.NullString
	)

	err := s.db.QueryRow(`SELECT token, scope_json, audience, runner_id, job_id, issuer, session_id, issued_at, expires_at, consumed_at
		FROM token_broker_tokens WHERE token = ?`, strings.TrimSpace(token)).Scan(
		&state.Token,
		&scopeJSON,
		&state.Audience,
		&state.RunnerID,
		&state.JobID,
		&state.Issuer,
		&state.SessionID,
		&issuedAt,
		&expiresAt,
		&consumedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrStoreTokenNotFound
		}
		return nil, fmt.Errorf("get token: %w", err)
	}

	if err := json.Unmarshal([]byte(scopeJSON), &state.Scope); err != nil {
		return nil, fmt.Errorf("decode scope: %w", err)
	}
	state.IssuedAt, err = time.Parse(time.RFC3339Nano, issuedAt)
	if err != nil {
		return nil, fmt.Errorf("parse issued_at: %w", err)
	}
	state.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse expires_at: %w", err)
	}
	if consumedAt.Valid && strings.TrimSpace(consumedAt.String) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, consumedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse consumed_at: %w", err)
		}
		state.ConsumedAt = &parsed
	}

	return &state, nil
}

// MarkConsumed sets consumed_at for a token when not consumed yet.
func (s *Store) MarkConsumed(token string, consumedAt time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("token broker store unavailable")
	}

	res, err := s.db.Exec(`UPDATE token_broker_tokens
		SET consumed_at = ?
		WHERE token = ? AND consumed_at IS NULL`,
		consumedAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(token),
	)
	if err != nil {
		return false, fmt.Errorf("mark consumed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark consumed rows affected: %w", err)
	}
	return n > 0, nil
}

// Close closes the underlying DB handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
