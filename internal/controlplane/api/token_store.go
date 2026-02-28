package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Token represents a registration token.
type Token struct {
	Value          string    `json:"token"`
	Created        time.Time `json:"created"`
	Expires        time.Time `json:"expires"`
	Used           bool      `json:"used"`
	MultiUse       bool      `json:"multi_use,omitempty"`
	InstallCommand string    `json:"install_command,omitempty"`
}

// TokenStore manages registration tokens.
type TokenStore struct {
	db        *sql.DB
	tokens    map[string]*Token
	secret    []byte
	serverURL string // used to generate install commands
	mu        sync.RWMutex
}

// GenerateOptions controls token generation behavior.
type GenerateOptions struct {
	MultiUse bool
	NoExpiry bool
}

// NewTokenStore opens (or creates) a SQLite-backed token store with a random HMAC secret.
func NewTokenStore(dbPath string) (*TokenStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open token db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tokens (
		value           TEXT PRIMARY KEY,
		created_at      TEXT NOT NULL,
		expires_at      TEXT NOT NULL,
		used            INTEGER NOT NULL DEFAULT 0,
		multi_use       INTEGER NOT NULL DEFAULT 0,
		install_command TEXT
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create tokens table: %w", err)
	}

	secret := make([]byte, 32)
	_, _ = rand.Read(secret)

	ts := &TokenStore{
		db:     db,
		tokens: make(map[string]*Token),
		secret: secret,
	}

	if err := ts.loadAll(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load tokens: %w", err)
	}

	return ts, nil
}

// SetServerURL sets the server URL used in install commands.
func (ts *TokenStore) SetServerURL(url string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.serverURL = strings.TrimRight(strings.TrimSpace(url), "/")
}

// Generate creates a new registration token valid for 30 minutes.
func (ts *TokenStore) Generate() *Token {
	return ts.GenerateWithOptions(GenerateOptions{})
}

// GenerateWithOptions creates a new registration token using options.
func (ts *TokenStore) GenerateWithOptions(opts GenerateOptions) *Token {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	now := time.Now().UTC()
	id := uuid.New().String()[:12]

	mac := hmac.New(sha256.New, ts.secret)
	mac.Write([]byte(id))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]

	expiry := now.Add(30 * time.Minute)
	if opts.NoExpiry {
		expiry = now.Add(100 * 365 * 24 * time.Hour)
	}

	token := &Token{
		Value:    fmt.Sprintf("prb_%s_%d_%s", id, now.Unix(), sig),
		Created:  now,
		Expires:  expiry,
		MultiUse: opts.MultiUse,
	}

	if ts.serverURL != "" {
		token.InstallCommand = installCommand(ts.serverURL, token.Value)
	}

	ts.tokens[token.Value] = token
	_ = ts.upsertToken(token)
	return token
}

// Consume validates and consumes a token. Returns false if invalid, expired, or already used.
func (ts *TokenStore) Consume(value string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	t, ok := ts.tokens[value]
	if !ok {
		return false
	}
	if time.Now().UTC().After(t.Expires) {
		return false
	}
	if t.Used {
		return false
	}
	if !t.MultiUse {
		t.Used = true
		_ = ts.updateUsed(t.Value, true)
	}
	return true
}

// ListActive returns all tokens that are still valid for registration.
func (ts *TokenStore) ListActive() []*Token {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	now := time.Now().UTC()
	var active []*Token
	for _, t := range ts.tokens {
		if !t.Used && now.Before(t.Expires) {
			active = append(active, t)
		}
	}
	return active
}

// Count returns the total number of tokens (active + used + expired).
func (ts *TokenStore) Count() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.tokens)
}

// Close shuts down the store.
func (ts *TokenStore) Close() error {
	if ts == nil || ts.db == nil {
		return nil
	}
	return ts.db.Close()
}

func (ts *TokenStore) upsertToken(token *Token) error {
	_, err := ts.db.Exec(`INSERT INTO tokens (value, created_at, expires_at, used, multi_use, install_command)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(value) DO UPDATE SET
			created_at = excluded.created_at,
			expires_at = excluded.expires_at,
			used = excluded.used,
			multi_use = excluded.multi_use,
			install_command = excluded.install_command`,
		token.Value,
		token.Created.Format(time.RFC3339Nano),
		token.Expires.Format(time.RFC3339Nano),
		boolToInt(token.Used),
		boolToInt(token.MultiUse),
		nullableString(token.InstallCommand),
	)
	return err
}

func (ts *TokenStore) updateUsed(value string, used bool) error {
	_, err := ts.db.Exec(`UPDATE tokens SET used = ? WHERE value = ?`, boolToInt(used), value)
	return err
}

func (ts *TokenStore) loadAll() error {
	rows, err := ts.db.Query(`SELECT value, created_at, expires_at, used, multi_use, install_command FROM tokens`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			value, createdAt, expiresAt string
			used, multiUse              int
			installCommand              sql.NullString
		)
		if err := rows.Scan(&value, &createdAt, &expiresAt, &used, &multiUse, &installCommand); err != nil {
			continue
		}

		created, err := parseTokenTime(createdAt)
		if err != nil {
			continue
		}
		expires, err := parseTokenTime(expiresAt)
		if err != nil {
			continue
		}

		t := &Token{
			Value:    value,
			Created:  created,
			Expires:  expires,
			Used:     used == 1,
			MultiUse: multiUse == 1,
		}
		if installCommand.Valid {
			t.InstallCommand = installCommand.String
		}
		ts.tokens[value] = t
	}

	return rows.Err()
}

func parseTokenTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
