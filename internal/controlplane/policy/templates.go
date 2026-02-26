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
	CreatedAt   time.Time                `json:"created_at"`
	UpdatedAt   time.Time                `json:"updated_at"`
}


// PolicyManager is the interface used by handlers for policy CRUD.
type PolicyManager interface {
	List() []*Template
	Get(id string) (*Template, bool)
	Create(name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string) *Template
	Update(id string, name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string) (*Template, error)
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
	s.templates["diagnose"] = &Template{
		ID:          "diagnose",
		Name:        "Diagnose",
		Description: "Read access plus diagnostic tools (strace, tcpdump, etc).",
		Level:       protocol.CapDiagnose,
		Blocked:     []string{"rm -rf", "mkfs", "dd if=", "reboot", "shutdown", "systemctl stop", "systemctl restart"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.templates["full-remediate"] = &Template{
		ID:          "full-remediate",
		Name:        "Full Remediate",
		Description: "Full access including service restarts and package management. Use with approval queue.",
		Level:       protocol.CapRemediate,
		Blocked:     []string{"rm -rf /", "mkfs", "dd if=/dev/zero of=/dev/sd"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
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
func (s *Store) Create(name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string) *Template {
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
	s.templates[id] = t
	return t
}

// Update modifies an existing template.
func (s *Store) Update(id string, name, description string, level protocol.CapabilityLevel, allowed, blocked, paths []string) (*Template, error) {
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
		PolicyID: t.ID,
		Level:    t.Level,
		Allowed:  t.Allowed,
		Blocked:  t.Blocked,
		Paths:    t.Paths,
	}
}
