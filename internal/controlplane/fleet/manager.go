// Package fleet manages the fleet of probes — registration, state, inventory.
package fleet

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// ProbeState represents the control plane view of a probe.
type ProbeState struct {
	ID          string                     `json:"id"`
	Hostname    string                     `json:"hostname"`
	OS          string                     `json:"os"`
	Arch        string                     `json:"arch"`
	Status      string                     `json:"status"` // pending, online, offline, degraded
	PolicyLevel protocol.CapabilityLevel   `json:"policy_level"`
	APIKey      string                     `json:"-"`
	Registered  time.Time                  `json:"registered"`
	LastSeen    time.Time                  `json:"last_seen"`
	Inventory   *protocol.InventoryPayload `json:"inventory,omitempty"`
	Labels      map[string]string          `json:"labels,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	Health      *HealthScore               `json:"health,omitempty"`
	lastHB      *protocol.HeartbeatPayload
}

// Manager tracks all probes in the fleet.
type Manager struct {
	probes map[string]*ProbeState
	mu     sync.RWMutex
	logger *zap.Logger
}

// NewManager creates a fleet manager.
func NewManager(logger *zap.Logger) *Manager {
	return &Manager{
		probes: make(map[string]*ProbeState),
		logger: logger,
	}
}

// Register adds a new probe to the fleet.
func (m *Manager) Register(id, hostname, os, arch string) *ProbeState {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	ps := &ProbeState{
		ID:          id,
		Hostname:    hostname,
		OS:          os,
		Arch:        arch,
		Status:      "online",
		PolicyLevel: protocol.CapObserve, // default: read-only
		Registered:  now,
		LastSeen:    now,
		Labels:      map[string]string{},
		Tags:        []string{},
	}
	m.probes[id] = ps
	m.logger.Info("probe registered",
		zap.String("id", id),
		zap.String("hostname", hostname),
	)
	return ps
}

// Heartbeat updates the last-seen time for a probe.
func (m *Manager) Heartbeat(id string, hb *protocol.HeartbeatPayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.probes[id]
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	ps.LastSeen = time.Now().UTC()
	ps.lastHB = hb

	// Compute health score
	h := ScoreHealth(hb, ps.Inventory)
	ps.Health = &h

	// Auto-detect degraded status
	if h.Status == "critical" || h.Status == "degraded" {
		ps.Status = "degraded"
	} else {
		ps.Status = "online"
	}
	return nil
}

// UpdateInventory stores a probe inventory report.
func (m *Manager) UpdateInventory(id string, inv *protocol.InventoryPayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.probes[id]
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	ps.Inventory = inv
	ps.LastSeen = time.Now().UTC()
	return nil
}

// Get returns a probe state.
func (m *Manager) Get(id string) (*ProbeState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ps, ok := m.probes[id]
	return ps, ok
}

// FindByHostname returns the best matching probe for a hostname.
// Preference order: online/degraded probes first, then most recently seen.
func (m *Manager) FindByHostname(hostname string) (*ProbeState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var best *ProbeState
	for _, candidate := range m.probes {
		if candidate.Hostname != hostname {
			continue
		}
		if best == nil || betterHostnameCandidate(candidate, best) {
			best = candidate
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}

// List returns all probes.
func (m *Manager) List() []*ProbeState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*ProbeState, 0, len(m.probes))
	for _, ps := range m.probes {
		result = append(result, ps)
	}
	return result
}

// SetPolicy updates a probe capability level.
func (m *Manager) SetPolicy(id string, level protocol.CapabilityLevel) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.probes[id]
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	ps.PolicyLevel = level
	m.logger.Info("policy updated",
		zap.String("id", id),
		zap.String("level", string(level)),
	)
	return nil
}

// SetAPIKey updates a probe API key in fleet state.
func (m *Manager) SetAPIKey(id, apiKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.probes[id]
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	ps.APIKey = apiKey
	return nil
}

// MarkOffline checks all probes and marks stale probes as offline.
func (m *Manager) MarkOffline(threshold time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().UTC().Add(-threshold)
	for _, ps := range m.probes {
		if ps.Status != "offline" && ps.LastSeen.Before(cutoff) {
			previousStatus := ps.Status
			ps.Status = "offline"
			m.logger.Warn("probe marked offline",
				zap.String("id", ps.ID),
				zap.String("previous_status", previousStatus),
				zap.Time("last_seen", ps.LastSeen),
			)
		}
	}
}

// SetOnline marks a probe online and refreshes last-seen.
func (m *Manager) SetOnline(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.probes[id]
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}

	ps.Status = "online"
	ps.LastSeen = time.Now().UTC()
	return nil
}

// Count returns the number of probes in each status.
func (m *Manager) Count() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	counts := map[string]int{}
	for _, ps := range m.probes {
		counts[ps.Status]++
	}
	return counts
}

// SetTags replaces probe tags with a normalized set.
func (m *Manager) SetTags(id string, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.probes[id]
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	ps.Tags = normalizeTags(tags)
	return nil
}

// ListByTag returns probes that contain the given tag.
func (m *Manager) ListByTag(tag string) []*ProbeState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return nil
	}
	out := make([]*ProbeState, 0)
	for _, ps := range m.probes {
		for _, t := range ps.Tags {
			if t == tag {
				out = append(out, ps)
				break
			}
		}
	}
	return out
}

// TagCounts returns fleet probe counts by tag.
func (m *Manager) TagCounts() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	counts := map[string]int{}
	for _, ps := range m.probes {
		for _, t := range ps.Tags {
			counts[t]++
		}
	}
	return counts
}

func betterHostnameCandidate(candidate, current *ProbeState) bool {
	candidateRank := hostnameStatusRank(candidate.Status)
	currentRank := hostnameStatusRank(current.Status)
	if candidateRank != currentRank {
		return candidateRank > currentRank
	}
	if !candidate.LastSeen.Equal(current.LastSeen) {
		return candidate.LastSeen.After(current.LastSeen)
	}
	if !candidate.Registered.Equal(current.Registered) {
		return candidate.Registered.After(current.Registered)
	}
	return candidate.ID < current.ID
}

func hostnameStatusRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online":
		return 4
	case "degraded":
		return 3
	case "pending":
		return 2
	case "offline":
		return 1
	default:
		return 0
	}
}

func normalizeTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// Delete removes a probe from the fleet entirely.
// Returns error if the probe does not exist.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.probes[id]; !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}
	delete(m.probes, id)
	return nil
}

// CleanupOffline removes all probes that have been offline longer than the given threshold.
// Returns the list of removed probe IDs.
func (m *Manager) CleanupOffline(olderThan time.Duration) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().UTC().Add(-olderThan)
	removed := []string{}
	for id, ps := range m.probes {
		if ps.Status == "offline" && ps.LastSeen.Before(cutoff) {
			delete(m.probes, id)
			removed = append(removed, id)
		}
	}
	return removed
}

// LastHeartbeat returns the last heartbeat payload for external consumers.
func (ps *ProbeState) LastHeartbeat() *protocol.HeartbeatPayload {
	return ps.lastHB
}
