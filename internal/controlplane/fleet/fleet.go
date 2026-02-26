package fleet

import (
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// Fleet defines the interface for fleet management operations.
// Both Manager (in-memory) and Store (SQLite-backed) implement this.
type Fleet interface {
	Register(id, hostname, os_, arch string) *ProbeState
	Heartbeat(id string, hb *protocol.HeartbeatPayload) error
	UpdateInventory(id string, inv *protocol.InventoryPayload) error
	Get(id string) (*ProbeState, bool)
	List() []*ProbeState
	SetPolicy(id string, level protocol.CapabilityLevel) error
	SetAPIKey(id, apiKey string) error
	MarkOffline(threshold time.Duration)
	Count() map[string]int
	SetTags(id string, tags []string) error
	ListByTag(tag string) []*ProbeState
	TagCounts() map[string]int
}

// compile-time interface checks
var _ Fleet = (*Manager)(nil)
var _ Fleet = (*Store)(nil)
