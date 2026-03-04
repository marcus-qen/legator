package sandbox

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RuntimeClass constants for sandbox template runtime classification.
const (
	RuntimeClassWASM   = "wasmtime"
	RuntimeClassKata   = "kata-containers"
	RuntimeClassNative = "runc"
)

// Capability constants used in template capability constraints.
const (
	CapNetworkAccess  = "network"
	CapHostFSWrite    = "host_fs_write"
	CapHostFSRead     = "host_fs_read"
	CapProcessSpawn   = "process_spawn"
	CapSyscallUnrestr = "syscall_unrestricted"
)

// WASMRestrictedCapabilities is the set of capabilities disallowed in the WASM lane.
// A template with RuntimeClass==wasmtime must not include any of these.
var WASMRestrictedCapabilities = []string{
	CapNetworkAccess,
	CapHostFSWrite,
	CapSyscallUnrestr,
}

// SandboxTemplate defines a reusable execution template for sandbox sessions.
// Templates capture runtime class, resource limits, and capability constraints.
type SandboxTemplate struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// RuntimeClass selects the sandbox runtime: wasmtime, kata-containers, or runc.
	RuntimeClass string `json:"runtime_class"`

	// Capabilities lists the capabilities explicitly allowed for this template.
	// An empty slice means the minimum baseline (most restrictive).
	Capabilities []string `json:"capabilities,omitempty"`

	// DeniedCapabilities lists capabilities explicitly blocked.
	DeniedCapabilities []string `json:"denied_capabilities,omitempty"`

	// Resource limits.
	CPUMillis  int `json:"cpu_millis,omitempty"`   // 0 = default
	MemoryMiB  int `json:"memory_mib,omitempty"`   // 0 = default
	MaxRunSecs int `json:"max_run_secs,omitempty"` // 0 = default

	// Metadata is an arbitrary map for user-defined annotations.
	Metadata map[string]string `json:"metadata,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ValidateCapabilities returns an error if the template's capability list is
// incompatible with its runtime class. WASM templates cannot include any
// WASMRestrictedCapabilities.
func (t *SandboxTemplate) ValidateCapabilities() error {
	if t.RuntimeClass != RuntimeClassWASM {
		return nil
	}
	restricted := make(map[string]struct{}, len(WASMRestrictedCapabilities))
	for _, c := range WASMRestrictedCapabilities {
		restricted[c] = struct{}{}
	}
	for _, cap := range t.Capabilities {
		if _, bad := restricted[cap]; bad {
			return fmt.Errorf("capability %q is not permitted in WASM lane (runtime_class=%s)", cap, RuntimeClassWASM)
		}
	}
	return nil
}

// TemplateStore provides SQLite persistence for SandboxTemplates.
// It is workspace-scoped: each workspace may define its own templates.
type TemplateStore struct {
	db *sql.DB
}

// NewTemplateStore opens (or creates) a SQLite-backed template store using the
// database handle shared with the sandbox session Store.
func NewTemplateStore(db *sql.DB) (*TemplateStore, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sandbox_templates (
		id                   TEXT    PRIMARY KEY,
		workspace_id         TEXT    NOT NULL DEFAULT '',
		name                 TEXT    NOT NULL,
		description          TEXT    NOT NULL DEFAULT '',
		runtime_class        TEXT    NOT NULL,
		capabilities         TEXT    NOT NULL DEFAULT '[]',
		denied_capabilities  TEXT    NOT NULL DEFAULT '[]',
		cpu_millis           INTEGER NOT NULL DEFAULT 0,
		memory_mib           INTEGER NOT NULL DEFAULT 0,
		max_run_secs         INTEGER NOT NULL DEFAULT 0,
		metadata             TEXT    NOT NULL DEFAULT '{}',
		created_at           TEXT    NOT NULL,
		updated_at           TEXT    NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("create sandbox_templates table: %w", err)
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tpl_workspace ON sandbox_templates(workspace_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tpl_runtime  ON sandbox_templates(runtime_class)`)

	ts := &TemplateStore{db: db}
	if err := ts.seedBuiltins(); err != nil {
		return nil, fmt.Errorf("seed builtin templates: %w", err)
	}
	return ts, nil
}

// seedBuiltins inserts the built-in WASM lane templates if they do not already exist.
func (ts *TemplateStore) seedBuiltins() error {
	builtins := builtinTemplates()
	for _, tpl := range builtins {
		existing, err := ts.Get(tpl.ID, "")
		if err == nil && existing != nil {
			continue // already present
		}
		if err := ts.upsert(tpl); err != nil {
			return fmt.Errorf("seed template %s: %w", tpl.ID, err)
		}
	}
	return nil
}

// builtinTemplates returns the two built-in WASM fast lane templates.
func builtinTemplates() []*SandboxTemplate {
	now := time.Now().UTC()
	return []*SandboxTemplate{
		{
			ID:                 "wasm-lint-check",
			WorkspaceID:        "",
			Name:               "WASM Lint Check",
			Description:        "Lightweight WASM linter template. Accepts input files and emits JSON findings. No network access, no host FS writes. Designed for fast static analysis in the WASM lane.",
			RuntimeClass:       RuntimeClassWASM,
			Capabilities:       []string{CapHostFSRead},
			DeniedCapabilities: WASMRestrictedCapabilities,
			CPUMillis:          250,
			MemoryMiB:          128,
			MaxRunSecs:         60,
			Metadata: map[string]string{
				"task_kind":   "lint",
				"output_type": "json_findings",
				"lane":        "wasm",
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:                 "wasm-json-transform",
			WorkspaceID:        "",
			Name:               "WASM JSON Transform",
			Description:        "Lightweight WASM template for deterministic JSON transformation. Accepts input JSON and a transform spec, emits transformed output JSON. No network, no host FS writes.",
			RuntimeClass:       RuntimeClassWASM,
			Capabilities:       []string{},
			DeniedCapabilities: WASMRestrictedCapabilities,
			CPUMillis:          250,
			MemoryMiB:          64,
			MaxRunSecs:         30,
			Metadata: map[string]string{
				"task_kind":   "transform",
				"output_type": "json",
				"lane":        "wasm",
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
}

// Create inserts a new SandboxTemplate. ID is generated if empty.
func (ts *TemplateStore) Create(tpl *SandboxTemplate) (*SandboxTemplate, error) {
	if err := tpl.ValidateCapabilities(); err != nil {
		return nil, err
	}
	if tpl.ID == "" {
		tpl.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	tpl.CreatedAt = now
	tpl.UpdatedAt = now

	if err := ts.upsert(tpl); err != nil {
		return nil, err
	}
	return tpl, nil
}

// Get returns a SandboxTemplate by ID. If workspaceID is non-empty, only
// templates owned by that workspace (or built-in global templates with empty
// workspace_id) are returned.
func (ts *TemplateStore) Get(id, workspaceID string) (*SandboxTemplate, error) {
	row := ts.db.QueryRow(`SELECT
		id, workspace_id, name, description, runtime_class, capabilities, denied_capabilities,
		cpu_millis, memory_mib, max_run_secs, metadata, created_at, updated_at
		FROM sandbox_templates WHERE id = ?`, id)
	tpl, err := scanTemplate(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Workspace isolation: built-ins (workspace_id == "") are always visible.
	if workspaceID != "" && tpl.WorkspaceID != "" && tpl.WorkspaceID != workspaceID {
		return nil, nil
	}
	return tpl, nil
}

// List returns templates visible to the given workspace (built-ins + workspace-owned).
func (ts *TemplateStore) List(workspaceID string) ([]*SandboxTemplate, error) {
	var rows *sql.Rows
	var err error
	if workspaceID == "" {
		rows, err = ts.db.Query(`SELECT
			id, workspace_id, name, description, runtime_class, capabilities, denied_capabilities,
			cpu_millis, memory_mib, max_run_secs, metadata, created_at, updated_at
			FROM sandbox_templates ORDER BY created_at ASC`)
	} else {
		rows, err = ts.db.Query(`SELECT
			id, workspace_id, name, description, runtime_class, capabilities, denied_capabilities,
			cpu_millis, memory_mib, max_run_secs, metadata, created_at, updated_at
			FROM sandbox_templates
			WHERE workspace_id = '' OR workspace_id = ?
			ORDER BY created_at ASC`, workspaceID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*SandboxTemplate
	for rows.Next() {
		tpl, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tpl)
	}
	return out, rows.Err()
}

// Update modifies an existing template. WorkspaceID is not changed.
func (ts *TemplateStore) Update(tpl *SandboxTemplate) error {
	if err := tpl.ValidateCapabilities(); err != nil {
		return err
	}
	tpl.UpdatedAt = time.Now().UTC()
	return ts.upsert(tpl)
}

// Delete removes a template by ID.
func (ts *TemplateStore) Delete(id string) error {
	res, err := ts.db.Exec(`DELETE FROM sandbox_templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("template not found: %s", id)
	}
	return nil
}

// ListByRuntimeClass returns all templates with the given runtime_class,
// filtered by workspace visibility.
func (ts *TemplateStore) ListByRuntimeClass(runtimeClass, workspaceID string) ([]*SandboxTemplate, error) {
	all, err := ts.List(workspaceID)
	if err != nil {
		return nil, err
	}
	var out []*SandboxTemplate
	for _, tpl := range all {
		if strings.EqualFold(tpl.RuntimeClass, runtimeClass) {
			out = append(out, tpl)
		}
	}
	return out, nil
}

func (ts *TemplateStore) upsert(tpl *SandboxTemplate) error {
	capsJSON, _ := json.Marshal(tpl.Capabilities)
	deniedJSON, _ := json.Marshal(tpl.DeniedCapabilities)
	metaJSON, _ := json.Marshal(tpl.Metadata)

	_, err := ts.db.Exec(`INSERT INTO sandbox_templates (
		id, workspace_id, name, description, runtime_class, capabilities, denied_capabilities,
		cpu_millis, memory_mib, max_run_secs, metadata, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name                = excluded.name,
		description         = excluded.description,
		runtime_class       = excluded.runtime_class,
		capabilities        = excluded.capabilities,
		denied_capabilities = excluded.denied_capabilities,
		cpu_millis          = excluded.cpu_millis,
		memory_mib          = excluded.memory_mib,
		max_run_secs        = excluded.max_run_secs,
		metadata            = excluded.metadata,
		updated_at          = excluded.updated_at`,
		tpl.ID,
		tpl.WorkspaceID,
		tpl.Name,
		tpl.Description,
		tpl.RuntimeClass,
		string(capsJSON),
		string(deniedJSON),
		tpl.CPUMillis,
		tpl.MemoryMiB,
		tpl.MaxRunSecs,
		string(metaJSON),
		tpl.CreatedAt.Format(time.RFC3339),
		tpl.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

type templateScanner interface {
	Scan(dest ...any) error
}

func scanTemplate(s templateScanner) (*SandboxTemplate, error) {
	var (
		id, workspaceID, name, desc, rc string
		capsJSON, deniedJSON, metaJSON  string
		cpu, mem, maxRun                int
		createdStr, updatedStr          string
	)
	if err := s.Scan(
		&id, &workspaceID, &name, &desc, &rc,
		&capsJSON, &deniedJSON,
		&cpu, &mem, &maxRun,
		&metaJSON, &createdStr, &updatedStr,
	); err != nil {
		return nil, err
	}

	var caps, denied []string
	_ = json.Unmarshal([]byte(capsJSON), &caps)
	_ = json.Unmarshal([]byte(deniedJSON), &denied)

	var meta map[string]string
	_ = json.Unmarshal([]byte(metaJSON), &meta)

	created, _ := time.Parse(time.RFC3339, createdStr)
	updated, _ := time.Parse(time.RFC3339, updatedStr)

	return &SandboxTemplate{
		ID:                 id,
		WorkspaceID:        workspaceID,
		Name:               name,
		Description:        desc,
		RuntimeClass:       rc,
		Capabilities:       caps,
		DeniedCapabilities: denied,
		CPUMillis:          cpu,
		MemoryMiB:          mem,
		MaxRunSecs:         maxRun,
		Metadata:           meta,
		CreatedAt:          created,
		UpdatedAt:          updated,
	}, nil
}
