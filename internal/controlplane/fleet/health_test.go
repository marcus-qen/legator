package fleet

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestHealthScoreHealthy(t *testing.T) {
	hb := &protocol.HeartbeatPayload{
		Load:      [3]float64{0.5, 0.3, 0.2},
		MemUsed:   2 * 1024 * 1024 * 1024, // 2 GB
		MemTotal:  8 * 1024 * 1024 * 1024, // 8 GB = 25%
		DiskUsed:  50 * 1024 * 1024 * 1024, // 50 GB
		DiskTotal: 200 * 1024 * 1024 * 1024, // 200 GB = 25%
	}
	inv := &protocol.InventoryPayload{CPUs: 4}

	h := ScoreHealth(hb, inv)
	if h.Score != 100 || h.Status != "healthy" {
		t.Fatalf("expected 100/healthy, got %d/%s warnings=%v", h.Score, h.Status, h.Warnings)
	}
	if len(h.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", h.Warnings)
	}
}

func TestHealthScoreHighMemory(t *testing.T) {
	hb := &protocol.HeartbeatPayload{
		Load:      [3]float64{0.5, 0.3, 0.2},
		MemUsed:   7 * 1024 * 1024 * 1024, // 7 GB = 87.5%
		MemTotal:  8 * 1024 * 1024 * 1024,
		DiskUsed:  50 * 1024 * 1024 * 1024,
		DiskTotal: 200 * 1024 * 1024 * 1024,
	}

	h := ScoreHealth(hb, nil)
	if h.Score != 85 {
		t.Fatalf("expected 85, got %d", h.Score)
	}
	if h.Status != "healthy" {
		t.Fatalf("expected healthy, got %s", h.Status)
	}
	if len(h.Warnings) != 1 || h.Warnings[0] != "high memory usage" {
		t.Fatalf("expected high memory warning, got %v", h.Warnings)
	}
}

func TestHealthScoreCriticalDisk(t *testing.T) {
	hb := &protocol.HeartbeatPayload{
		Load:      [3]float64{0.5, 0.3, 0.2},
		MemUsed:   2 * 1024 * 1024 * 1024,
		MemTotal:  8 * 1024 * 1024 * 1024,
		DiskUsed:  196 * 1024 * 1024 * 1024, // 98%
		DiskTotal: 200 * 1024 * 1024 * 1024,
	}

	h := ScoreHealth(hb, nil)
	if h.Score != 70 {
		t.Fatalf("expected 70, got %d", h.Score)
	}
	if h.Status != "warning" {
		t.Fatalf("expected warning, got %s", h.Status)
	}
}

func TestHealthScoreMultipleCritical(t *testing.T) {
	hb := &protocol.HeartbeatPayload{
		Load:      [3]float64{16.0, 12.0, 8.0}, // extreme load
		MemUsed:   7800 * 1024 * 1024,           // 96% of 8 GB
		MemTotal:  8 * 1024 * 1024 * 1024,
		DiskUsed:  196 * 1024 * 1024 * 1024,     // 98%
		DiskTotal: 200 * 1024 * 1024 * 1024,
	}
	inv := &protocol.InventoryPayload{CPUs: 2}

	h := ScoreHealth(hb, inv)
	if h.Score > 20 {
		t.Fatalf("expected critical (<=20), got %d", h.Score)
	}
	if h.Status != "critical" {
		t.Fatalf("expected critical, got %s", h.Status)
	}
	if len(h.Warnings) < 3 {
		t.Fatalf("expected 3+ warnings, got %v", h.Warnings)
	}
}

func TestHealthScoreNilHeartbeat(t *testing.T) {
	h := ScoreHealth(nil, nil)
	if h.Score != 0 || h.Status != "unknown" {
		t.Fatalf("expected 0/unknown, got %d/%s", h.Score, h.Status)
	}
}

func TestHealthScoreZeroTotals(t *testing.T) {
	// Edge case: totals are 0 (shouldn't divide by zero)
	hb := &protocol.HeartbeatPayload{
		Load:     [3]float64{1.0, 0.5, 0.3},
		MemUsed:  0,
		MemTotal: 0,
	}
	h := ScoreHealth(hb, nil)
	if h.Score < 0 {
		t.Fatal("score should not be negative")
	}
}
