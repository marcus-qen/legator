package fleet

import (
	"github.com/marcus-qen/legator/internal/protocol"
)

// HealthScore represents a probe's health assessment.
type HealthScore struct {
	Score    int      `json:"score"`    // 0-100 (100 = perfect)
	Status   string   `json:"status"`   // healthy, warning, degraded, critical
	Warnings []string `json:"warnings,omitempty"`
}

// Thresholds for health scoring.
const (
	loadHighThreshold     = 4.0  // 1-minute load average
	loadCritThreshold     = 8.0
	memHighPct            = 85.0 // memory usage %
	memCritPct            = 95.0
	diskHighPct           = 80.0 // disk usage %
	diskCritPct           = 95.0
)

// ScoreHealth computes a health score from heartbeat + inventory data.
func ScoreHealth(hb *protocol.HeartbeatPayload, inv *protocol.InventoryPayload) HealthScore {
	score := 100
	var warnings []string

	if hb == nil {
		return HealthScore{Score: 0, Status: "unknown", Warnings: []string{"no heartbeat data"}}
	}

	// Load average check (1-minute)
	load := hb.Load[0]
	cpus := 1
	if inv != nil && inv.CPUs > 0 {
		cpus = inv.CPUs
	}
	loadPerCPU := load / float64(cpus)
	if loadPerCPU >= loadCritThreshold/float64(cpus) {
		score -= 30
		warnings = append(warnings, "critical load average")
	} else if loadPerCPU >= loadHighThreshold/float64(cpus) {
		score -= 15
		warnings = append(warnings, "high load average")
	}

	// Memory check
	if hb.MemTotal > 0 {
		memPct := float64(hb.MemUsed) / float64(hb.MemTotal) * 100
		if memPct >= memCritPct {
			score -= 30
			warnings = append(warnings, "critical memory usage")
		} else if memPct >= memHighPct {
			score -= 15
			warnings = append(warnings, "high memory usage")
		}
	}

	// Disk check
	if hb.DiskTotal > 0 {
		diskPct := float64(hb.DiskUsed) / float64(hb.DiskTotal) * 100
		if diskPct >= diskCritPct {
			score -= 30
			warnings = append(warnings, "critical disk usage")
		} else if diskPct >= diskHighPct {
			score -= 15
			warnings = append(warnings, "high disk usage")
		}
	}

	if score < 0 {
		score = 0
	}

	status := "healthy"
	switch {
	case score >= 80:
		status = "healthy"
	case score >= 50:
		status = "warning"
	case score >= 20:
		status = "degraded"
	default:
		status = "critical"
	}

	return HealthScore{Score: score, Status: status, Warnings: warnings}
}
