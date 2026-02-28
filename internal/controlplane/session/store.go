package session

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const DefaultSessionLifetime = 24 * time.Hour

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExpired  = errors.New("session expired")
)

// Session is an authenticated user session.
type Session struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastActive time.Time `json:"last_active"`
}

// Store manages sessions in SQLite.
type Store struct {
	db       *sql.DB
	lifetime time.Duration
}

// NewStore opens a SQLite-backed session store and migrates schema.
func NewStore(dbPath string, sessionLifetime time.Duration) (*Store, error) {
	if sessionLifetime <= 0 {
		sessionLifetime = DefaultSessionLifetime
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sessions db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		id          TEXT PRIMARY KEY,
		user_id     TEXT NOT NULL,
		created_at  TEXT NOT NULL,
		expires_at  TEXT NOT NULL,
		last_active TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create sessions table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at)`)

	return &Store{db: db, lifetime: sessionLifetime}, nil
}

// Create creates a new session for a user.
func (s *Store) Create(userID string) (*Session, error) {
	token, err := generateToken(32)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	sess := &Session{
		ID:         token,
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.lifetime),
		LastActive: now,
	}

	_, err = s.db.Exec(`INSERT INTO sessions (id, user_id, created_at, expires_at, last_active)
		VALUES (?, ?, ?, ?, ?)`,
		sess.ID,
		sess.UserID,
		sess.CreatedAt.Format(time.RFC3339Nano),
		sess.ExpiresAt.Format(time.RFC3339Nano),
		sess.LastActive.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return sess, nil
}

// Validate validates a session token, checks expiry, and refreshes last_active.
func (s *Store) Validate(token string) (*Session, error) {
	sess, err := s.get(token)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if now.After(sess.ExpiresAt) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, token)
		return nil, ErrSessionExpired
	}

	if _, err := s.db.Exec(`UPDATE sessions SET last_active = ? WHERE id = ?`, now.Format(time.RFC3339Nano), token); err != nil {
		return nil, fmt.Errorf("update last_active: %w", err)
	}

	sess.LastActive = now
	return sess, nil
}

// Delete deletes a session by token.
func (s *Store) Delete(token string) error {
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, token); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteByUser deletes all sessions for a user.
func (s *Store) DeleteByUser(userID string) error {
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete sessions by user: %w", err)
	}
	return nil
}

// Cleanup deletes expired sessions and returns deleted row count.
func (s *Store) Cleanup() (int, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("cleanup sessions: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cleanup rows affected: %w", err)
	}

	return int(n), nil
}

// Close closes the underlying DB.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) get(token string) (*Session, error) {
	var (
		sess       Session
		createdAt  string
		expiresAt  string
		lastActive string
	)

	err := s.db.QueryRow(`SELECT id, user_id, created_at, expires_at, last_active FROM sessions WHERE id = ?`, token).Scan(
		&sess.ID, &sess.UserID, &createdAt, &expiresAt, &lastActive,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("get session: %w", err)
	}

	sess.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	sess.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse expires_at: %w", err)
	}
	sess.LastActive, err = time.Parse(time.RFC3339Nano, lastActive)
	if err != nil {
		return nil, fmt.Errorf("parse last_active: %w", err)
	}

	return &sess, nil
}

func generateToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
