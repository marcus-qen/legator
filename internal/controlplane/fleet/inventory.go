package fleet

import (
	"sort"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// InventoryFilter limits fleet inventory queries.
type InventoryFilter struct {
	Tag    string `json:"tag,omitempty"`
	Status string `json:"status,omitempty"`
}

// ProbeInventorySummary is the per-probe payload returned by FleetInventory.
type ProbeInventorySummary struct {
	ID          string                   `json:"id"`
	Hostname    string                   `json:"hostname"`
	Status      string                   `json:"status"`
	OS          string                   `json:"os"`
	Arch        string                   `json:"arch"`
	Kernel      string                   `json:"kernel,omitempty"`
	PolicyLevel protocol.CapabilityLevel `json:"policy_level"`
	Tags        []string                 `json:"tags,omitempty"`
	LastSeen    time.Time                `json:"last_seen"`
	CollectedAt time.Time                `json:"collected_at,omitempty"`
	CPUs        int                      `json:"cpus"`
	RAMBytes    uint64                   `json:"ram_bytes"`
	DiskBytes   uint64                   `json:"disk_bytes"`
}

// FleetAggregates summarizes fleet totals across the selected probes.
type FleetAggregates struct {
	TotalProbes     int            `json:"total_probes"`
	Online          int            `json:"online"`
	TotalCPUs       int            `json:"total_cpus"`
	TotalRAMBytes   uint64         `json:"total_ram_bytes"`
	ProbesByOS      map[string]int `json:"probes_by_os"`
	TagDistribution map[string]int `json:"tag_distribution"`
}

// FleetInventory is the API shape for fleet-wide inventory views.
type FleetInventory struct {
	Probes     []ProbeInventorySummary `json:"probes"`
	Aggregates FleetAggregates         `json:"aggregates"`
}

// Inventory returns probe inventory summaries with optional filters.
func (m *Manager) Inventory(filter InventoryFilter) FleetInventory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statusFilter := strings.ToLower(strings.TrimSpace(filter.Status))
	tagFilter := strings.ToLower(strings.TrimSpace(filter.Tag))

	result := FleetInventory{
		Probes: make([]ProbeInventorySummary, 0, len(m.probes)),
		Aggregates: FleetAggregates{
			ProbesByOS:      map[string]int{},
			TagDistribution: map[string]int{},
		},
	}

	for _, ps := range m.probes {
		if statusFilter != "" && strings.ToLower(ps.Status) != statusFilter {
			continue
		}
		if tagFilter != "" && !hasTag(ps.Tags, tagFilter) {
			continue
		}

		summary := toInventorySummary(ps)
		result.Probes = append(result.Probes, summary)

		result.Aggregates.TotalProbes++
		if strings.EqualFold(summary.Status, "online") {
			result.Aggregates.Online++
		}
		result.Aggregates.TotalCPUs += summary.CPUs
		result.Aggregates.TotalRAMBytes += summary.RAMBytes

		osKey := strings.ToLower(strings.TrimSpace(summary.OS))
		if osKey == "" {
			osKey = "unknown"
		}
		result.Aggregates.ProbesByOS[osKey]++

		for _, tag := range summary.Tags {
			result.Aggregates.TagDistribution[tag]++
		}
	}

	sort.Slice(result.Probes, func(i, j int) bool {
		lhs := strings.ToLower(strings.TrimSpace(result.Probes[i].Hostname))
		rhs := strings.ToLower(strings.TrimSpace(result.Probes[j].Hostname))
		if lhs == "" {
			lhs = result.Probes[i].ID
		}
		if rhs == "" {
			rhs = result.Probes[j].ID
		}
		return lhs < rhs
	})

	return result
}

func toInventorySummary(ps *ProbeState) ProbeInventorySummary {
	summary := ProbeInventorySummary{
		ID:          ps.ID,
		Hostname:    ps.Hostname,
		Status:      ps.Status,
		OS:          ps.OS,
		Arch:        ps.Arch,
		PolicyLevel: ps.PolicyLevel,
		Tags:        append([]string(nil), ps.Tags...),
		LastSeen:    ps.LastSeen,
	}

	if ps.Inventory == nil {
		return summary
	}

	if summary.Hostname == "" {
		summary.Hostname = ps.Inventory.Hostname
	}
	if ps.Inventory.OS != "" {
		summary.OS = ps.Inventory.OS
	}
	if ps.Inventory.Arch != "" {
		summary.Arch = ps.Inventory.Arch
	}

	summary.Kernel = ps.Inventory.Kernel
	summary.CollectedAt = ps.Inventory.CollectedAt
	summary.CPUs = ps.Inventory.CPUs
	summary.RAMBytes = ps.Inventory.MemTotal
	summary.DiskBytes = ps.Inventory.DiskTotal

	return summary
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if strings.EqualFold(tag, want) {
			return true
		}
	}
	return false
}
