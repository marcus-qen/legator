// Package tenant provides multi-tenant isolation for MSP deployments.
// Each probe belongs to exactly one tenant; each user belongs to one or more.
package tenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
	_ "modernc.org/sqlite"
)

// Errors returned by Store operations.
var (
	ErrTenantNotFound = errors.New("tenant not found")
	ErrSlugConflict   = errors.New("tenant slug already exists")
)

// Tenant is an isolated MSP customer/organisational unit.
type Tenant struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	ContactEmail string    `json:"contact_email"`
	CreatedAt    time.Time `json:"created_at"`
}

// Store manages tenants and user-tenant memberships in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) the SQLite-backed tenant store and runs migrations.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open tenant db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	runner := migration.NewRunner("tenant", []migration.Migration{
		{
			Version:     1,
			Description: "initial tenant schema",
			Up: func(tx *sql.Tx) error {
				if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS tenants (
					id            TEXT PRIMARY KEY,
					name          TEXT NOT NULL,
					slug          TEXT NOT NULL UNIQUE,
					contact_email TEXT NOT NULL DEFAULT '',
					created_at    TEXT NOT NULL
				)`); err != nil {
					return err
				}
				if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_tenants_slug ON tenants(slug)`); err != nil {
					return err
				}
				if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS user_tenants (
					user_id   TEXT NOT NULL,
					tenant_id TEXT NOT NULL,
					PRIMARY KEY (user_id, tenant_id)
				)`); err != nil {
					return err
				}
				_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_user_tenants_user ON user_tenants(user_id)`)
				return err
			},
		},
	})
	if err := runner.Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate tenant db: %w", err)
	}

	return &Store{db: db}, nil
}

// Create creates a new tenant.
func (s *Store) Create(name, slug, contactEmail string) (*Tenant, error) {
	slug = NormalizeSlug(slug)
	if slug == "" {
		return nil, fmt.Errorf("slug required")
	}
	if name = strings.TrimSpace(name); name == "" {
		return nil, fmt.Errorf("name required")
	}
	t := &Tenant{
		ID:           uuid.NewString(),
		Name:         name,
		Slug:         slug,
		ContactEmail: strings.TrimSpace(contactEmail),
		CreatedAt:    time.Now().UTC(),
	}
	_, err := s.db.Exec(
		`INSERT INTO tenants (id, name, slug, contact_email, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Slug, t.ContactEmail, t.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: tenants.slug") {
			return nil, ErrSlugConflict
		}
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	return t, nil
}

// Get returns a tenant by ID.
func (s *Store) Get(id string) (*Tenant, error) {
	return s.queryOne(
		`SELECT id, name, slug, contact_email, created_at FROM tenants WHERE id = ?`, id,
	)
}

// GetBySlug returns a tenant by slug.
func (s *Store) GetBySlug(slug string) (*Tenant, error) {
	return s.queryOne(
		`SELECT id, name, slug, contact_email, created_at FROM tenants WHERE slug = ?`,
		NormalizeSlug(slug),
	)
}

// List returns all tenants ordered by name.
func (s *Store) List() ([]*Tenant, error) {
	rows, err := s.db.Query(
		`SELECT id, name, slug, contact_email, created_at FROM tenants ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var tenants []*Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, t)
	}
	if tenants == nil {
		tenants = []*Tenant{}
	}
	return tenants, rows.Err()
}

// Update updates a tenant's mutable fields (name and contact_email).
// Slug is immutable after creation.
func (s *Store) Update(id, name, contactEmail string) (*Tenant, error) {
	if name = strings.TrimSpace(name); name == "" {
		return nil, fmt.Errorf("name required")
	}
	res, err := s.db.Exec(
		`UPDATE tenants SET name = ?, contact_email = ? WHERE id = ?`,
		name, strings.TrimSpace(contactEmail), id,
	)
	if err != nil {
		return nil, fmt.Errorf("update tenant: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrTenantNotFound
	}
	return s.Get(id)
}

// Delete removes a tenant. The caller is responsible for ensuring no probes
// reference the tenant before calling Delete.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM tenants WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTenantNotFound
	}
	// Clean up memberships.
	_, _ = s.db.Exec(`DELETE FROM user_tenants WHERE tenant_id = ?`, id)
	return nil
}

// SetUserTenants replaces the full set of tenants for a user atomically.
func (s *Store) SetUserTenants(userID string, tenantIDs []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_tenants WHERE user_id = ?`, userID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear user tenants: %w", err)
	}
	for _, tid := range tenantIDs {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO user_tenants (user_id, tenant_id) VALUES (?, ?)`, userID, tid,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert user tenant: %w", err)
		}
	}
	return tx.Commit()
}

// GetUserTenants returns the tenant IDs the user belongs to, sorted.
func (s *Store) GetUserTenants(userID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT tenant_id FROM user_tenants WHERE user_id = ? ORDER BY tenant_id`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("get user tenants: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ProbesInTenant returns the number of probes whose tenant_id matches id,
// queried from the provided probeCountFn (injected to avoid circular imports).
// Pass nil to skip the probe count check.
func (s *Store) Close() error {
	return s.db.Close()
}

// NormalizeSlug lowercases, trims spaces, and replaces spaces/underscores with hyphens.
func NormalizeSlug(slug string) string {
	s := strings.ToLower(strings.TrimSpace(slug))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	return s
}

func (s *Store) queryOne(query string, args ...any) (*Tenant, error) {
	row := s.db.QueryRow(query, args...)
	return scanTenant(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTenant(row rowScanner) (*Tenant, error) {
	var t Tenant
	var createdAt string
	if err := row.Scan(&t.ID, &t.Name, &t.Slug, &t.ContactEmail, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTenantNotFound
		}
		return nil, fmt.Errorf("scan tenant: %w", err)
	}
	ts, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
	}
	t.CreatedAt = ts
	return &t, nil
}

// ── Context helpers ──────────────────────────────────────────────────────────

type contextKey string

const scopeContextKey contextKey = "tenantScope"

// Scope holds the tenant filtering context for a request.
type Scope struct {
	// TenantIDs are the tenants visible to the current user.
	// Ignored when IsAdmin is true.
	TenantIDs []string
	// IsAdmin grants visibility across all tenants.
	IsAdmin bool
}

// WithScope attaches scope to ctx.
func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeContextKey, scope)
}

// ScopeFromContext retrieves the Scope from ctx.
// Returns zero Scope (no tenant access) if not set.
func ScopeFromContext(ctx context.Context) Scope {
	s, _ := ctx.Value(scopeContextKey).(Scope)
	return s
}

// AllowsAll reports whether the scope grants cross-tenant visibility (admin).
func (s Scope) AllowsAll() bool {
	return s.IsAdmin
}

// AllowsTenant reports whether the scope permits seeing probes in tenantID.
// Empty tenantID probes are only visible to admins.
func (s Scope) AllowsTenant(tenantID string) bool {
	if s.IsAdmin {
		return true
	}
	for _, id := range s.TenantIDs {
		if id == tenantID {
			return true
		}
	}
	return false
}
