// Package policy manages reusable policy templates for the fleet.
package policy

import (
	"fmt"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// Template defines a named, reusable policy configuration.
type Template struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	Level       protocol.CapabilityLevel `json:"level"`
	Allowed     []string                 `json:"allowed,omitempty"`
	Blocked     []string                 `json:"blocked,omitempty"`
	Paths       []string                 `json:"paths,omitempty"`

	ExecutionClassRequired protocol.ExecutionClass   `json:"execution_class_required,omitempty"`
	SandboxRequired        bool                      `json:"sandbox_required"`
	ApprovalMode           protocol.ApprovalMode     `json:"approval_mode,omitempty"`
	RequireSecondApprover  bool                      `json:"require_second_approver,omitempty"`
	Breakglass             protocol.BreakglassPolicy `json:"breakglass,omitempty"`
	MaxRuntimeSec          int                       `json:"max_runtime_sec,omitempty"`
	AllowedScopes          []string                  `json:"allowed_scopes,omitempty"`

	// WASM lane runtime configuration.
	RuntimeClass        string   `json:"runtime_class,omitempty"`
	CPUMillis           int      `json:"cpu_millis,omitempty"`
	MemoryMiB           int      `json:"memory_mib,omitempty"`
	AllowedCapabilities []string `json:"allowed_capabilities,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TemplateOptions contains additive policy v2 fields.
type TemplateOptions struct {
	ExecutionClassRequired   protocol.ExecutionClass
	SandboxRequired          bool
	ApprovalMode             protocol.ApprovalMode
	RequireSecondApprover    bool
	RequireSecondApproverSet bool
	Breakglass               protocol.BreakglassPolicy
	MaxRuntimeSec            int
	AllowedScopes            []string

	// WASM lane resource constraints.
	RuntimeClass        string
	CPUMillis           int
	MemoryMiB           int
	AllowedCapabilities []string
}

// PolicyManager is the interface used by handlers for policy CRUD.
type PolicyManager interface {
	List() []*Template
	Get(id string) (*Template, bool)
	Create(name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string, opts TemplateOptions) *Template
	Update(id string, name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string, opts TemplateOptions) (*Template, error)
	Delete(id string) error
}

// Store manages policy templates.
type Store struct {
	templates map[string]*Template // keyed by ID
	mu        sync.RWMutex
	nextID    int
}

// NewStore creates a policy template store with built-in defaults.
func NewStore() *Store {
	s := &Store{
		templates: make(map[string]*Template),
		nextID:    100,
	}
	// Built-in templates
	now := time.Now().UTC()
	s.templates["observe-only"] = &Template{
		ID:          "observe-only",
		Name:        "Observe Only",
		Description: "Read-only access. Cannot modify system state.",
		Level:       protocol.CapObserve,
		Blocked:     []string{"rm", "kill", "reboot", "shutdown", "mkfs", "dd", "systemctl stop", "systemctl restart"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.applyOptions(s.templates["observe-only"], TemplateOptions{
		ExecutionClassRequired: protocol.ExecObserveDirect,
		SandboxRequired:        false,
		ApprovalMode:           protocol.ApprovalNone,
	})

	s.templates["diagnose"] = &Template{
		ID:          "diagnose",
		Name:        "Diagnose",
		Description: "Read access plus diagnostic tools (strace, tcpdump, etc).",
		Level:       protocol.CapDiagnose,
		Blocked:     []string{"rm -rf", "mkfs", "dd if=", "reboot", "shutdown", "systemctl stop", "systemctl restart"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.applyOptions(s.templates["diagnose"], TemplateOptions{
		ExecutionClassRequired: protocol.ExecDiagnoseSandbox,
		SandboxRequired:        true,
		ApprovalMode:           protocol.ApprovalMutationGate,
	})

	s.templates["full-remediate"] = &Template{
		ID:          "full-remediate",
		Name:        "Full Remediate",
		Description: "Full access including service restarts and package management. Use with approval queue.",
		Level:       protocol.CapRemediate,
		Blocked:     []string{"rm -rf /", "mkfs", "dd if=/dev/zero of=/dev/sd"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.applyOptions(s.templates["full-remediate"], TemplateOptions{
		ExecutionClassRequired: protocol.ExecRemediateSandbox,
		SandboxRequired:        true,
		ApprovalMode:           protocol.ApprovalMutationGate,
	})

	s.templates["wasm-fast-lane"] = &Template{
		ID:                  "wasm-fast-lane",
		Name:                "WASM Fast Lane",
		Description:         "Lightweight WASM sandbox with wasmtime runtime class. Minimal capabilities, strict resource limits, no host-direct mutations.",
		Level:               protocol.CapDiagnose,
		Blocked:             []string{"rm -rf", "mkfs", "dd", "reboot", "shutdown", "mount", "chroot"},
		RuntimeClass:        "wasmtime",
		CPUMillis:           500,
		MemoryMiB:           256,
		AllowedCapabilities: []string{},
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	s.applyOptions(s.templates["wasm-fast-lane"], TemplateOptions{
		ExecutionClassRequired: protocol.ExecWasmSandbox,
		SandboxRequired:        true,
		ApprovalMode:           protocol.ApprovalMutationGate,
		MaxRuntimeSec:          300,
		AllowedScopes:          []string{"wasm:exec", "wasm:read"},
	})
	return s
}

// List returns all templates.
func (s *Store) List() []*Template {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Template, 0, len(s.templates))
	for _, t := range s.templates {
		out = append(out, t)
	}
	return out
}

// Get returns a template by ID.
func (s *Store) Get(id string) (*Template, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.templates[id]
	return t, ok
}

// Create adds a new template.
func (s *Store) Create(name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string, opts TemplateOptions) *Template {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := fmt.Sprintf("pol-%d", s.nextID)
	now := time.Now().UTC()
	t := &Template{
		ID:          id,
		Name:        name,
		Description: description,
		Level:       level,
		Allowed:     allowed,
		Blocked:     blocked,
		Paths:       paths,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.applyOptions(t, opts)
	s.templates[id] = t
	return t
}

// Update modifies an existing template.
func (s *Store) Update(id string, name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string, opts TemplateOptions) (*Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.templates[id]
	if !ok {
		return nil, fmt.Errorf("template not found: %s", id)
	}
	t.Name = name
	t.Description = description
	t.Level = level
	t.Allowed = allowed
	t.Blocked = blocked
	t.Paths = paths
	s.applyOptions(t, opts)
	t.UpdatedAt = time.Now().UTC()
	return t, nil
}

// Delete removes a template.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.templates[id]; !ok {
		return fmt.Errorf("template not found: %s", id)
	}
	delete(s.templates, id)
	return nil
}

// ToPolicy converts a template to a PolicyUpdatePayload for sending to a probe.
func (t *Template) ToPolicy() *protocol.PolicyUpdatePayload {
	return &protocol.PolicyUpdatePayload{
		PolicyID:               t.ID,
		Level:                  t.Level,
		Allowed:                t.Allowed,
		Blocked:                t.Blocked,
		Paths:                  t.Paths,
		ExecutionClassRequired: t.ExecutionClassRequired,
		SandboxRequired:        t.SandboxRequired,
		ApprovalMode:           t.ApprovalMode,
		RequireSecondApprover:  t.RequireSecondApprover,
		Breakglass:             t.Breakglass,
		MaxRuntimeSec:          t.MaxRuntimeSec,
		AllowedScopes:          append([]string(nil), t.AllowedScopes...),
	}
}

func (s *Store) applyOptions(tpl *Template, opts TemplateOptions) {
	if tpl == nil {
		return
	}

	opts = MergeTemplateOptions(DefaultTemplateOptionsForLevel(tpl.Level), opts)
	opts = NormalizeTemplateOptions(opts)
	tpl.ExecutionClassRequired = opts.ExecutionClassRequired
	tpl.SandboxRequired = opts.SandboxRequired
	tpl.ApprovalMode = opts.ApprovalMode
	tpl.RequireSecondApprover = opts.RequireSecondApprover
	tpl.Breakglass = opts.Breakglass
	tpl.MaxRuntimeSec = opts.MaxRuntimeSec
	tpl.AllowedScopes = append([]string(nil), opts.AllowedScopes...)
	if opts.RuntimeClass != "" {
		tpl.RuntimeClass = opts.RuntimeClass
	}
	if opts.CPUMillis != 0 {
		tpl.CPUMillis = opts.CPUMillis
	}
	if opts.MemoryMiB != 0 {
		tpl.MemoryMiB = opts.MemoryMiB
	}
	if opts.AllowedCapabilities != nil {
		tpl.AllowedCapabilities = append([]string(nil), opts.AllowedCapabilities...)
	}
}
