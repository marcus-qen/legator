package users

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var (
	ErrUserNotFound        = errors.New("user not found")
	ErrInvalidRole         = errors.New("invalid role")
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrUserDisabled        = errors.New("user disabled")
	ErrUsernameAlreadyUsed = errors.New("username already exists")
)

// User is a control plane user account.
type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	DisplayName  string     `json:"display_name"`
	PasswordHash string     `json:"-"`
	Role         string     `json:"role"`
	Enabled      bool       `json:"enabled"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLogin    *time.Time `json:"last_login,omitempty"`
}

// Store manages users persisted in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) the SQLite-backed user store and migrates schema.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open users db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id            TEXT PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE,
		display_name  TEXT NOT NULL,
		password_hash TEXT NOT NULL,
		role          TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
		enabled       INTEGER NOT NULL DEFAULT 1,
		created_at    TEXT NOT NULL,
		last_login    TEXT
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create users table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)`)

	return &Store{db: db}, nil
}

// Create creates a new user with a generated UUID ID and bcrypt password hash.
func (s *Store) Create(username, displayName, password, role string) (*User, error) {
	return s.CreateWithID(uuid.NewString(), username, displayName, password, role)
}

// CreateWithID creates a new user with an explicit ID.
func (s *Store) CreateWithID(id, username, displayName, password, role string) (*User, error) {
	if !validRole(role) {
		return nil, ErrInvalidRole
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("username required")
	}
	if id == "" {
		id = uuid.NewString()
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC()
	u := &User{
		ID:           id,
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: string(hash),
		Role:         role,
		Enabled:      true,
		CreatedAt:    now,
	}

	_, err = s.db.Exec(`INSERT INTO users (id, username, display_name, password_hash, role, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?)`,
		u.ID, u.Username, u.DisplayName, u.PasswordHash, u.Role, u.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
			return nil, ErrUsernameAlreadyUsed
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	return u, nil
}

// Get fetches a user by ID.
func (s *Store) Get(id string) (*User, error) {
	u, err := s.queryOne(`SELECT id, username, display_name, password_hash, role, enabled, created_at, last_login FROM users WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetByUsername fetches a user by username.
func (s *Store) GetByUsername(username string) (*User, error) {
	u, err := s.queryOne(`SELECT id, username, display_name, password_hash, role, enabled, created_at, last_login FROM users WHERE username = ?`, username)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// List returns all users.
func (s *Store) List() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, display_name, password_hash, role, enabled, created_at, last_login FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users rows: %w", err)
	}

	return users, nil
}

// UpdatePassword updates a user's password hash.
func (s *Store) UpdatePassword(id, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	res, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	return checkRowsAffected(res, ErrUserNotFound)
}

// UpdateRole updates a user's role.
func (s *Store) UpdateRole(id, role string) error {
	if !validRole(role) {
		return ErrInvalidRole
	}

	res, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}

	return checkRowsAffected(res, ErrUserNotFound)
}

// UpdateProfile updates username and display name.
func (s *Store) UpdateProfile(id, username, displayName string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username required")
	}

	res, err := s.db.Exec(`UPDATE users SET username = ?, display_name = ? WHERE id = ?`, username, displayName, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
			return ErrUsernameAlreadyUsed
		}
		return fmt.Errorf("update profile: %w", err)
	}

	return checkRowsAffected(res, ErrUserNotFound)
}

// SetEnabled enables/disables a user account.
func (s *Store) SetEnabled(id string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	res, err := s.db.Exec(`UPDATE users SET enabled = ? WHERE id = ?`, enabledInt, id)
	if err != nil {
		return fmt.Errorf("set enabled: %w", err)
	}

	return checkRowsAffected(res, ErrUserNotFound)
}

// Delete permanently removes a user.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}

	return checkRowsAffected(res, ErrUserNotFound)
}

// Authenticate checks username/password and updates last_login.
func (s *Store) Authenticate(username, password string) (*User, error) {
	u, err := s.GetByUsername(username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	if !u.Enabled {
		return nil, ErrUserDisabled
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	now := time.Now().UTC()
	if _, err := s.db.Exec(`UPDATE users SET last_login = ? WHERE id = ?`, now.Format(time.RFC3339Nano), u.ID); err != nil {
		return nil, fmt.Errorf("update last_login: %w", err)
	}

	u.LastLogin = &now
	return u, nil
}

// Count returns total number of users.
func (s *Store) Count() int {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0
	}
	return count
}

// Close closes the underlying DB.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) queryOne(query string, args ...any) (*User, error) {
	row := s.db.QueryRow(query, args...)
	u, err := scanUser(row)
	if err != nil {
		return nil, err
	}
	return u, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(s scanner) (*User, error) {
	var (
		u                    User
		enabled              int
		createdAt, lastLogin sql.NullString
	)

	if err := s.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &enabled, &createdAt, &lastLogin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}

	u.Enabled = enabled == 1
	if createdAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, createdAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		u.CreatedAt = t
	}
	if lastLogin.Valid {
		t, err := time.Parse(time.RFC3339Nano, lastLogin.String)
		if err != nil {
			return nil, fmt.Errorf("parse last_login: %w", err)
		}
		u.LastLogin = &t
	}

	return &u, nil
}

func checkRowsAffected(res sql.Result, errWhenZero error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errWhenZero
	}
	return nil
}

func validRole(role string) bool {
	switch role {
	case "admin", "operator", "viewer":
		return true
	default:
		return false
	}
}
