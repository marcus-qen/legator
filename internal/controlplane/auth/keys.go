// Package auth provides API key management for multi-user authentication.
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"golang.org/x/crypto/bcrypt"
)

// Permission defines what an API key can do.
type Permission string

const (
	PermFleetRead     Permission = "fleet:read"
	PermFleetWrite    Permission = "fleet:write"
	PermCommandExec   Permission = "command:exec"
	PermApprovalRead  Permission = "approval:read"
	PermApprovalWrite Permission = "approval:write"
	PermAuditRead     Permission = "audit:read"
	PermWebhookManage Permission = "webhook:manage"
	PermAdmin         Permission = "admin" // all permissions
)

// APIKey represents a stored API key.
type APIKey struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	KeyHash     string       `json:"-"`           // never exposed
	KeyPrefix   string       `json:"key_prefix"`  // first 8 chars for identification
	Permissions []Permission `json:"permissions"`
	CreatedAt   time.Time    `json:"created_at"`
	LastUsedAt  *time.Time   `json:"last_used_at,omitempty"`
	ExpiresAt   *time.Time   `json:"expires_at,omitempty"`
	Enabled     bool         `json:"enabled"`
}

// KeyStore manages API keys with SQLite backing.
type KeyStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewKeyStore opens (or creates) a SQLite-backed key store.
func NewKeyStore(dbPath string) (*KeyStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open auth db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		key_hash    TEXT NOT NULL,
		key_prefix  TEXT NOT NULL,
		permissions TEXT NOT NULL DEFAULT '[]',
		created_at  TEXT NOT NULL,
		last_used   TEXT,
		expires_at  TEXT,
		enabled     INTEGER NOT NULL DEFAULT 1
	)`); err != nil {
		db.Close()
		return nil, err
	}

	db.Exec(`CREATE INDEX IF NOT EXISTS idx_keys_prefix ON api_keys(key_prefix)`)

	return &KeyStore{db: db}, nil
}

// Create generates a new API key, stores the bcrypt hash, and returns the plaintext once.
func (ks *KeyStore) Create(name string, permissions []Permission, expiresAt *time.Time) (*APIKey, string, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	// Generate random key
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate key: %w", err)
	}
	plainKey := "lgk_" + hex.EncodeToString(raw)

	// bcrypt hash
	hash, err := bcrypt.GenerateFromPassword([]byte(plainKey), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", fmt.Errorf("hash key: %w", err)
	}

	now := time.Now().UTC()
	key := &APIKey{
		ID:          uuid.NewString(),
		Name:        name,
		KeyHash:     string(hash),
		KeyPrefix:   plainKey[:12], // "lgk_" + 8 hex chars
		Permissions: permissions,
		CreatedAt:   now,
		Enabled:     true,
		ExpiresAt:   expiresAt,
	}

	permsJSON := permissionsToJSON(permissions)
	var expiresStr sql.NullString
	if expiresAt != nil {
		expiresStr = sql.NullString{String: expiresAt.Format(time.RFC3339Nano), Valid: true}
	}

	_, err = ks.db.Exec(`INSERT INTO api_keys (id, name, key_hash, key_prefix, permissions, created_at, expires_at, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
		key.ID, key.Name, key.KeyHash, key.KeyPrefix, permsJSON,
		now.Format(time.RFC3339Nano), expiresStr)
	if err != nil {
		return nil, "", fmt.Errorf("store key: %w", err)
	}

	return key, plainKey, nil
}

// Validate checks a plaintext key, returning the APIKey if valid.
func (ks *KeyStore) Validate(plainKey string) (*APIKey, error) {
	if len(plainKey) < 12 {
		return nil, fmt.Errorf("invalid key format")
	}

	prefix := plainKey[:12]
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	var (
		key                                               APIKey
		permsJSON, createdAt                              string
		lastUsed, expiresAt                               sql.NullString
		enabled                                           int
	)

	err := ks.db.QueryRow(`SELECT id, name, key_hash, key_prefix, permissions, created_at, last_used, expires_at, enabled
		FROM api_keys WHERE key_prefix = ?`, prefix).Scan(
		&key.ID, &key.Name, &key.KeyHash, &key.KeyPrefix, &permsJSON,
		&createdAt, &lastUsed, &expiresAt, &enabled)
	if err != nil {
		return nil, fmt.Errorf("key not found")
	}

	key.Enabled = enabled == 1
	key.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	key.Permissions = jsonToPermissions(permsJSON)
	if lastUsed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		key.LastUsedAt = &t
	}
	if expiresAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, expiresAt.String)
		key.ExpiresAt = &t
	}

	// Check enabled
	if !key.Enabled {
		return nil, fmt.Errorf("key disabled")
	}

	// Check expiry
	if key.ExpiresAt != nil && time.Now().UTC().After(*key.ExpiresAt) {
		return nil, fmt.Errorf("key expired")
	}

	// Verify bcrypt hash
	if err := bcrypt.CompareHashAndPassword([]byte(key.KeyHash), []byte(plainKey)); err != nil {
		return nil, fmt.Errorf("invalid key")
	}

	// Update last_used
	now := time.Now().UTC()
	key.LastUsedAt = &now
	go func() {
		ks.mu.Lock()
		defer ks.mu.Unlock()
		ks.db.Exec(`UPDATE api_keys SET last_used = ? WHERE id = ?`,
			now.Format(time.RFC3339Nano), key.ID)
	}()

	return &key, nil
}

// List returns all API keys (without hashes).
func (ks *KeyStore) List() []APIKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	rows, err := ks.db.Query(`SELECT id, name, key_prefix, permissions, created_at, last_used, expires_at, enabled FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var (
			key                      APIKey
			permsJSON, createdAt     string
			lastUsed, expiresAt      sql.NullString
			enabled                  int
		)
		if err := rows.Scan(&key.ID, &key.Name, &key.KeyPrefix, &permsJSON, &createdAt, &lastUsed, &expiresAt, &enabled); err != nil {
			continue
		}
		key.Enabled = enabled == 1
		key.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		key.Permissions = jsonToPermissions(permsJSON)
		if lastUsed.Valid {
			t, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
			key.LastUsedAt = &t
		}
		if expiresAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, expiresAt.String)
			key.ExpiresAt = &t
		}
		keys = append(keys, key)
	}
	return keys
}

// Revoke disables a key.
func (ks *KeyStore) Revoke(id string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	res, err := ks.db.Exec(`UPDATE api_keys SET enabled = 0 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("key not found: %s", id)
	}
	return nil
}

// Delete removes a key entirely.
func (ks *KeyStore) Delete(id string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	res, err := ks.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("key not found: %s", id)
	}
	return nil
}

// HasPermission checks whether an API key has a specific permission.
func HasPermission(key *APIKey, perm Permission) bool {
	if key == nil {
		return false
	}
	for _, p := range key.Permissions {
		if p == PermAdmin || p == perm {
			return true
		}
	}
	return false
}

// Close shuts down the store.
func (ks *KeyStore) Close() error {
	return ks.db.Close()
}

func permissionsToJSON(perms []Permission) string {
	if len(perms) == 0 {
		return "[]"
	}
	s := "["
	for i, p := range perms {
		if i > 0 {
			s += ","
		}
		s += `"` + string(p) + `"`
	}
	return s + "]"
}

func jsonToPermissions(raw string) []Permission {
	if raw == "" || raw == "[]" {
		return nil
	}
	// Simple parser — avoid json import for this
	var perms []Permission
	inQuote := false
	current := ""
	for _, c := range raw {
		switch {
		case c == '"':
			if inQuote {
				perms = append(perms, Permission(current))
				current = ""
			}
			inQuote = !inQuote
		case inQuote:
			current += string(c)
		}
	}
	return perms
}
