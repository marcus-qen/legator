package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/migration"
	_ "modernc.org/sqlite"
)

var (
	// ErrCustomRoleNotFound is returned when a custom role does not exist.
	ErrCustomRoleNotFound = errors.New("custom role not found")
	// ErrCustomRoleExists is returned when a custom role with that name already exists.
	ErrCustomRoleExists = errors.New("custom role already exists")
	// ErrBuiltInRole is returned when an operation is attempted on a built-in role.
	ErrBuiltInRole = errors.New("cannot modify a built-in role")
)

// CustomRole represents a user-defined permission set.
type CustomRole struct {
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
	Description string       `json:"description"`
	CreatedAt   time.Time    `json:"created_at"`
}

// CustomRoleStore persists custom roles in SQLite.
type CustomRoleStore struct {
	db *sql.DB
}

// NewCustomRoleStore opens (or creates) the SQLite-backed custom role store.
func NewCustomRoleStore(dbPath string) (*CustomRoleStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open roles db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	runner := migration.NewRunner("custom_roles", []migration.Migration{
		{
			Version:     1,
			Description: "initial custom roles schema",
			Up: func(tx *sql.Tx) error {
				_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS custom_roles (
					name        TEXT PRIMARY KEY,
					permissions TEXT NOT NULL DEFAULT '',
					description TEXT NOT NULL DEFAULT '',
					created_at  TEXT NOT NULL
				)`)
				return err
			},
		},
	})
	if err := runner.Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate custom roles db: %w", err)
	}

	return &CustomRoleStore{db: db}, nil
}

// Create creates a new custom role. Returns ErrCustomRoleExists if name is taken.
// Returns ErrBuiltInRole if name conflicts with a built-in role.
func (s *CustomRoleStore) Create(name string, permissions []Permission, description string) (*CustomRole, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("role name required")
	}
	if IsBuiltInRole(name) {
		return nil, ErrBuiltInRole
	}

	now := time.Now().UTC()
	permsStr := encodePermissions(permissions)

	_, err := s.db.Exec(
		`INSERT INTO custom_roles (name, permissions, description, created_at) VALUES (?, ?, ?, ?)`,
		name, permsStr, description, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, ErrCustomRoleExists
		}
		return nil, fmt.Errorf("create custom role: %w", err)
	}

	return &CustomRole{
		Name:        name,
		Permissions: permissions,
		Description: description,
		CreatedAt:   now,
	}, nil
}

// Get retrieves a custom role by name.
func (s *CustomRoleStore) Get(name string) (*CustomRole, error) {
	var (
		permsStr    string
		description string
		createdAt   string
	)
	err := s.db.QueryRow(
		`SELECT permissions, description, created_at FROM custom_roles WHERE name = ?`, name,
	).Scan(&permsStr, &description, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCustomRoleNotFound
		}
		return nil, fmt.Errorf("get custom role: %w", err)
	}

	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		t = time.Time{}
	}

	return &CustomRole{
		Name:        name,
		Permissions: decodePermissions(permsStr),
		Description: description,
		CreatedAt:   t,
	}, nil
}

// List returns all custom roles ordered by name.
func (s *CustomRoleStore) List() ([]CustomRole, error) {
	rows, err := s.db.Query(`SELECT name, permissions, description, created_at FROM custom_roles ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list custom roles: %w", err)
	}
	defer rows.Close()

	var roles []CustomRole
	for rows.Next() {
		var (
			name, permsStr, description, createdAt string
		)
		if err := rows.Scan(&name, &permsStr, &description, &createdAt); err != nil {
			return nil, fmt.Errorf("scan custom role: %w", err)
		}
		t, _ := time.Parse(time.RFC3339Nano, createdAt)
		roles = append(roles, CustomRole{
			Name:        name,
			Permissions: decodePermissions(permsStr),
			Description: description,
			CreatedAt:   t,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list custom roles rows: %w", err)
	}
	if roles == nil {
		roles = []CustomRole{}
	}
	return roles, nil
}

// Update replaces the permissions and description of an existing custom role.
func (s *CustomRoleStore) Update(name string, permissions []Permission, description string) (*CustomRole, error) {
	name = strings.TrimSpace(name)
	if IsBuiltInRole(name) {
		return nil, ErrBuiltInRole
	}

	permsStr := encodePermissions(permissions)
	res, err := s.db.Exec(
		`UPDATE custom_roles SET permissions = ?, description = ? WHERE name = ?`,
		permsStr, description, name,
	)
	if err != nil {
		return nil, fmt.Errorf("update custom role: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrCustomRoleNotFound
	}

	return s.Get(name)
}

// Delete removes a custom role. Returns ErrBuiltInRole for built-in roles.
func (s *CustomRoleStore) Delete(name string) error {
	if IsBuiltInRole(name) {
		return ErrBuiltInRole
	}

	res, err := s.db.Exec(`DELETE FROM custom_roles WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete custom role: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrCustomRoleNotFound
	}
	return nil
}

// GetPermissions returns permissions for a custom role, or nil if not found.
func (s *CustomRoleStore) GetPermissions(name string) []Permission {
	cr, err := s.Get(name)
	if err != nil {
		return nil
	}
	return cr.Permissions
}

// Close closes the underlying database.
func (s *CustomRoleStore) Close() error {
	return s.db.Close()
}

func encodePermissions(perms []Permission) string {
	parts := make([]string, len(perms))
	for i, p := range perms {
		parts[i] = string(p)
	}
	return strings.Join(parts, ",")
}

func decodePermissions(raw string) []Permission {
	if strings.TrimSpace(raw) == "" {
		return []Permission{}
	}
	parts := strings.Split(raw, ",")
	perms := make([]Permission, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			perms = append(perms, Permission(p))
		}
	}
	return perms
}
