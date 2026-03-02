package policy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/migration"
	"github.com/marcus-qen/legator/internal/protocol"
	_ "modernc.org/sqlite"
)

// PersistentStore wraps Store with SQLite persistence.
type PersistentStore struct {
	*Store
	db *sql.DB
}

// NewPersistentStore opens (or creates) a SQLite-backed policy store.
func NewPersistentStore(dbPath string) (*PersistentStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open policy db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	runner := migration.NewRunner("policy", []migration.Migration{
		{
			Version:     1,
			Description: "initial policy template schema",
			Up: func(tx *sql.Tx) error {
				if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS policy_templates (
					id          TEXT PRIMARY KEY,
					name        TEXT NOT NULL,
					description TEXT NOT NULL DEFAULT '',
					level       TEXT NOT NULL,
					allowed     TEXT NOT NULL DEFAULT '[]',
					blocked     TEXT NOT NULL DEFAULT '[]',
					paths       TEXT NOT NULL DEFAULT '[]',
					created_at  TEXT NOT NULL,
					updated_at  TEXT NOT NULL
				)`); err != nil {
					return err
				}
				return nil
			},
		},
		{
			Version:     2,
			Description: "add policy v2 schema fields",
			Up: func(tx *sql.Tx) error {
				if err := addColumn(tx, `ALTER TABLE policy_templates ADD COLUMN execution_class_required TEXT NOT NULL DEFAULT ''`); err != nil {
					return err
				}
				if err := addColumn(tx, `ALTER TABLE policy_templates ADD COLUMN sandbox_required INTEGER NOT NULL DEFAULT 0`); err != nil {
					return err
				}
				if err := addColumn(tx, `ALTER TABLE policy_templates ADD COLUMN approval_mode TEXT NOT NULL DEFAULT ''`); err != nil {
					return err
				}
				if err := addColumn(tx, `ALTER TABLE policy_templates ADD COLUMN breakglass_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
					return err
				}
				if err := addColumn(tx, `ALTER TABLE policy_templates ADD COLUMN max_runtime_sec INTEGER NOT NULL DEFAULT 0`); err != nil {
					return err
				}
				if err := addColumn(tx, `ALTER TABLE policy_templates ADD COLUMN allowed_scopes TEXT NOT NULL DEFAULT '[]'`); err != nil {
					return err
				}

				_, err := tx.Exec(`UPDATE policy_templates
					SET
						execution_class_required = CASE level
							WHEN 'observe' THEN 'observe_direct'
							WHEN 'diagnose' THEN 'diagnose_sandbox'
							WHEN 'remediate' THEN 'remediate_sandbox'
							ELSE 'observe_direct'
						END,
						sandbox_required = CASE level
							WHEN 'diagnose' THEN 1
							WHEN 'remediate' THEN 1
							ELSE 0
						END,
						approval_mode = CASE level
							WHEN 'observe' THEN 'none'
							WHEN 'diagnose' THEN 'mutation_gate'
							WHEN 'remediate' THEN 'mutation_gate'
							ELSE 'none'
						END,
						breakglass_json = CASE
							WHEN trim(breakglass_json) = '' THEN '{}'
							ELSE breakglass_json
						END,
						allowed_scopes = CASE
							WHEN trim(allowed_scopes) = '' THEN '[]'
							ELSE allowed_scopes
						END
					WHERE COALESCE(execution_class_required, '') = '' OR COALESCE(approval_mode, '') = ''`)
				return err
			},
		},
	})
	if err := runner.Migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate policy db: %w", err)
	}

	base := NewStore()
	ps := &PersistentStore{Store: base, db: db}

	if err := ps.loadFromDB(); err != nil {
		db.Close()
		return nil, err
	}
	return ps, nil
}

// Create adds a template and persists it.
func (ps *PersistentStore) Create(name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string, opts TemplateOptions) *Template {
	t := ps.Store.Create(name, description, level, allowed, blocked, paths, opts)
	_ = ps.persist(t)
	return t
}

// Update modifies a template and persists it.
func (ps *PersistentStore) Update(id string, name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string, opts TemplateOptions) (*Template, error) {
	t, err := ps.Store.Update(id, name, description, level, allowed, blocked, paths, opts)
	if err != nil {
		return nil, err
	}
	_ = ps.persist(t)
	return t, nil
}

// Delete removes a template from both memory and disk.
func (ps *PersistentStore) Delete(id string) error {
	if err := ps.Store.Delete(id); err != nil {
		return err
	}
	_, _ = ps.db.Exec(`DELETE FROM policy_templates WHERE id = ?`, id)
	return nil
}

// Close shuts down the database.
func (ps *PersistentStore) Close() error {
	return ps.db.Close()
}

func (ps *PersistentStore) persist(t *Template) error {
	allowedJSON, _ := json.Marshal(t.Allowed)
	blockedJSON, _ := json.Marshal(t.Blocked)
	pathsJSON, _ := json.Marshal(t.Paths)
	breakglassJSON, _ := json.Marshal(t.Breakglass)
	allowedScopesJSON, _ := json.Marshal(t.AllowedScopes)

	_, err := ps.db.Exec(`INSERT INTO policy_templates (
			id, name, description, level, allowed, blocked, paths,
			execution_class_required, sandbox_required, approval_mode, breakglass_json, max_runtime_sec, allowed_scopes,
			created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			level = excluded.level,
			allowed = excluded.allowed,
			blocked = excluded.blocked,
			paths = excluded.paths,
			execution_class_required = excluded.execution_class_required,
			sandbox_required = excluded.sandbox_required,
			approval_mode = excluded.approval_mode,
			breakglass_json = excluded.breakglass_json,
			max_runtime_sec = excluded.max_runtime_sec,
			allowed_scopes = excluded.allowed_scopes,
			updated_at = excluded.updated_at`,
		t.ID,
		t.Name,
		t.Description,
		string(t.Level),
		string(allowedJSON),
		string(blockedJSON),
		string(pathsJSON),
		string(t.ExecutionClassRequired),
		boolToInt(t.SandboxRequired),
		string(t.ApprovalMode),
		string(breakglassJSON),
		t.MaxRuntimeSec,
		string(allowedScopesJSON),
		t.CreatedAt.Format(time.RFC3339),
		t.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (ps *PersistentStore) loadFromDB() error {
	rows, err := ps.db.Query(`SELECT
		id, name, description, level, allowed, blocked, paths,
		execution_class_required, sandbox_required, approval_mode, breakglass_json, max_runtime_sec, allowed_scopes,
		created_at, updated_at
		FROM policy_templates`)
	if err != nil {
		return err
	}
	defer rows.Close()

	ps.Store.mu.Lock()
	defer ps.Store.mu.Unlock()

	for rows.Next() {
		var (
			id, name, desc, level               string
			allowedJSON, blockedJSON, pathsJSON string
			executionClass, approvalMode        string
			sandboxRequired                     int
			breakglassJSON, allowedScopesJSON   string
			maxRuntimeSec                       int
			createdStr, updatedStr              string
		)
		if err := rows.Scan(
			&id, &name, &desc, &level,
			&allowedJSON, &blockedJSON, &pathsJSON,
			&executionClass, &sandboxRequired, &approvalMode, &breakglassJSON, &maxRuntimeSec, &allowedScopesJSON,
			&createdStr, &updatedStr,
		); err != nil {
			continue
		}

		var allowed, blocked, paths []string
		_ = json.Unmarshal([]byte(allowedJSON), &allowed)
		_ = json.Unmarshal([]byte(blockedJSON), &blocked)
		_ = json.Unmarshal([]byte(pathsJSON), &paths)

		defaults := DefaultTemplateOptionsForLevel(protocol.CapabilityLevel(level))
		opts := TemplateOptions{
			ExecutionClassRequired: protocol.ExecutionClass(strings.TrimSpace(executionClass)),
			SandboxRequired:        sandboxRequired != 0,
			ApprovalMode:           protocol.ApprovalMode(strings.TrimSpace(approvalMode)),
			MaxRuntimeSec:          maxRuntimeSec,
		}
		if opts.ExecutionClassRequired == "" {
			opts.ExecutionClassRequired = defaults.ExecutionClassRequired
		}
		if opts.ApprovalMode == "" {
			opts.ApprovalMode = defaults.ApprovalMode
		}
		if strings.TrimSpace(executionClass) == "" && strings.TrimSpace(approvalMode) == "" && !opts.SandboxRequired {
			opts.SandboxRequired = defaults.SandboxRequired
		}
		if strings.TrimSpace(breakglassJSON) != "" {
			_ = json.Unmarshal([]byte(breakglassJSON), &opts.Breakglass)
		}
		if strings.TrimSpace(allowedScopesJSON) != "" {
			_ = json.Unmarshal([]byte(allowedScopesJSON), &opts.AllowedScopes)
		}
		opts = NormalizeTemplateOptions(opts)

		created, _ := time.Parse(time.RFC3339, createdStr)
		updated, _ := time.Parse(time.RFC3339, updatedStr)

		ps.Store.templates[id] = &Template{
			ID:                     id,
			Name:                   name,
			Description:            desc,
			Level:                  protocol.CapabilityLevel(level),
			Allowed:                allowed,
			Blocked:                blocked,
			Paths:                  paths,
			ExecutionClassRequired: opts.ExecutionClassRequired,
			SandboxRequired:        opts.SandboxRequired,
			ApprovalMode:           opts.ApprovalMode,
			Breakglass:             opts.Breakglass,
			MaxRuntimeSec:          opts.MaxRuntimeSec,
			AllowedScopes:          opts.AllowedScopes,
			CreatedAt:              created,
			UpdatedAt:              updated,
		}
	}

	return rows.Err()
}

func addColumn(tx *sql.Tx, stmt string) error {
	_, err := tx.Exec(stmt)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
