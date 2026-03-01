package fleet

import (
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestRegisterAndGet(t *testing.T) {
	m := NewManager(testLogger())

	ps := m.Register("probe-1", "web-01", "linux", "amd64")
	if ps.ID != "probe-1" {
		t.Errorf("expected probe-1, got %s", ps.ID)
	}
	if ps.Status != "online" {
		t.Errorf("expected online, got %s", ps.Status)
	}
	if ps.PolicyLevel != protocol.CapObserve {
		t.Errorf("expected observe, got %s", ps.PolicyLevel)
	}

	got, ok := m.Get("probe-1")
	if !ok || got.Hostname != "web-01" {
		t.Error("Get failed")
	}
}

func TestFindByHostname(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")
	m.Register("probe-2", "db-01", "linux", "amd64")

	ps, ok := m.FindByHostname("web-01")
	if !ok {
		t.Fatal("expected to find probe by hostname")
	}
	if ps.ID != "probe-1" {
		t.Fatalf("expected probe-1, got %s", ps.ID)
	}

	if _, ok := m.FindByHostname("missing"); ok {
		t.Fatal("expected missing hostname to return not found")
	}
}

func TestFindByHostname_PrefersHealthyRecentCandidate(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-old-offline", "dup-host", "linux", "amd64")
	m.Register("probe-active", "dup-host", "linux", "amd64")

	m.mu.Lock()
	m.probes["probe-old-offline"].Status = "offline"
	m.probes["probe-old-offline"].LastSeen = time.Now().UTC().Add(-2 * time.Hour)
	m.probes["probe-active"].Status = "online"
	m.probes["probe-active"].LastSeen = time.Now().UTC().Add(-2 * time.Minute)
	m.mu.Unlock()

	ps, ok := m.FindByHostname("dup-host")
	if !ok {
		t.Fatal("expected to find duplicate hostname")
	}
	if ps.ID != "probe-active" {
		t.Fatalf("expected online probe to win duplicate hostname lookup, got %s", ps.ID)
	}
}

func TestHeartbeat(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")

	err := m.Heartbeat("probe-1", &protocol.HeartbeatPayload{ProbeID: "probe-1"})
	if err != nil {
		t.Errorf("heartbeat failed: %v", err)
	}

	err = m.Heartbeat("nonexistent", &protocol.HeartbeatPayload{})
	if err == nil {
		t.Error("expected error for unknown probe")
	}
}

func TestMarkOffline(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")

	// Manually backdate last_seen
	m.mu.Lock()
	m.probes["probe-1"].LastSeen = time.Now().UTC().Add(-2 * time.Minute)
	m.mu.Unlock()

	m.MarkOffline(60 * time.Second)

	ps, _ := m.Get("probe-1")
	if ps.Status != "offline" {
		t.Errorf("expected offline, got %s", ps.Status)
	}
}

func TestMarkOffline_TransitionsDegradedProbe(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-degraded", "db-01", "linux", "amd64")

	m.mu.Lock()
	m.probes["probe-degraded"].Status = "degraded"
	m.probes["probe-degraded"].LastSeen = time.Now().UTC().Add(-3 * time.Minute)
	m.mu.Unlock()

	m.MarkOffline(60 * time.Second)

	ps, _ := m.Get("probe-degraded")
	if ps.Status != "offline" {
		t.Fatalf("expected degraded probe to transition offline, got %s", ps.Status)
	}
}

func TestSetOnline(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")

	oldSeen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.mu.Lock()
	m.probes["probe-1"].Status = "offline"
	m.probes["probe-1"].LastSeen = oldSeen
	m.mu.Unlock()

	if err := m.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online failed: %v", err)
	}

	ps, _ := m.Get("probe-1")
	if ps.Status != "online" {
		t.Fatalf("expected online, got %s", ps.Status)
	}
	if !ps.LastSeen.After(oldSeen) {
		t.Fatalf("expected LastSeen to be updated, got %s", ps.LastSeen)
	}

	if err := m.SetOnline("missing"); err == nil {
		t.Fatal("expected error for unknown probe")
	}
}

func TestSetPolicy(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")

	err := m.SetPolicy("probe-1", protocol.CapRemediate)
	if err != nil {
		t.Errorf("set policy failed: %v", err)
	}

	ps, _ := m.Get("probe-1")
	if ps.PolicyLevel != protocol.CapRemediate {
		t.Errorf("expected remediate, got %s", ps.PolicyLevel)
	}
}

func TestList(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")
	m.Register("probe-2", "db-01", "linux", "arm64")

	list := m.List()
	if len(list) != 2 {
		t.Errorf("expected 2 probes, got %d", len(list))
	}
}

func TestCount(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")
	m.Register("probe-2", "db-01", "linux", "arm64")

	// Backdate one
	m.mu.Lock()
	m.probes["probe-2"].LastSeen = time.Now().UTC().Add(-5 * time.Minute)
	m.mu.Unlock()
	m.MarkOffline(60 * time.Second)

	counts := m.Count()
	if counts["online"] != 1 || counts["offline"] != 1 {
		t.Errorf("expected 1 online + 1 offline, got %v", counts)
	}
}

func TestSetTagsAndListByTag(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")
	m.Register("probe-2", "db-01", "linux", "amd64")

	if err := m.SetTags("probe-1", []string{"Prod", "Web", "prod"}); err != nil {
		t.Fatalf("set tags failed: %v", err)
	}
	if err := m.SetTags("probe-2", []string{"prod", "db"}); err != nil {
		t.Fatalf("set tags failed: %v", err)
	}

	prod := m.ListByTag("prod")
	if len(prod) != 2 {
		t.Fatalf("expected 2 prod probes, got %d", len(prod))
	}
	web := m.ListByTag("web")
	if len(web) != 1 || web[0].ID != "probe-1" {
		t.Fatalf("expected probe-1 for web tag, got %#v", web)
	}
}

func TestTagCounts(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")
	m.Register("probe-2", "db-01", "linux", "amd64")
	_ = m.SetTags("probe-1", []string{"prod", "web"})
	_ = m.SetTags("probe-2", []string{"prod", "db"})

	counts := m.TagCounts()
	if counts["prod"] != 2 || counts["web"] != 1 || counts["db"] != 1 {
		t.Fatalf("unexpected tag counts: %#v", counts)
	}
}

func TestSetAPIKey(t *testing.T) {
	m := NewManager(testLogger())
	m.Register("probe-1", "web-01", "linux", "amd64")

	if err := m.SetAPIKey("probe-1", "lgk_test_key"); err != nil {
		t.Fatalf("set api key failed: %v", err)
	}

	ps, _ := m.Get("probe-1")
	if ps.APIKey != "lgk_test_key" {
		t.Fatalf("expected api key to be updated, got %q", ps.APIKey)
	}

	if err := m.SetAPIKey("missing", "x"); err == nil {
		t.Fatal("expected error for unknown probe")
	}
}

func TestInventoryAggregatesAndFilters(t *testing.T) {
	m := NewManager(testLogger())

	m.Register("probe-1", "web-01", "linux", "amd64")
	m.Register("probe-2", "db-01", "linux", "amd64")
	m.Register("probe-3", "win-01", "windows", "amd64")

	_ = m.SetTags("probe-1", []string{"prod", "k8s-host"})
	_ = m.SetTags("probe-2", []string{"prod"})
	_ = m.SetTags("probe-3", []string{"lab"})

	_ = m.UpdateInventory("probe-1", &protocol.InventoryPayload{CPUs: 4, MemTotal: 8 * 1024 * 1024 * 1024, DiskTotal: 120 * 1024 * 1024 * 1024, OS: "linux"})
	_ = m.UpdateInventory("probe-2", &protocol.InventoryPayload{CPUs: 2, MemTotal: 4 * 1024 * 1024 * 1024, DiskTotal: 80 * 1024 * 1024 * 1024, OS: "linux"})
	_ = m.UpdateInventory("probe-3", &protocol.InventoryPayload{CPUs: 8, MemTotal: 16 * 1024 * 1024 * 1024, DiskTotal: 240 * 1024 * 1024 * 1024, OS: "windows"})

	m.mu.Lock()
	m.probes["probe-2"].Status = "degraded"
	m.probes["probe-3"].Status = "offline"
	m.mu.Unlock()

	all := m.Inventory(InventoryFilter{})
	if all.Aggregates.TotalProbes != 3 {
		t.Fatalf("expected 3 probes, got %d", all.Aggregates.TotalProbes)
	}
	if all.Aggregates.Online != 1 {
		t.Fatalf("expected 1 online probe, got %d", all.Aggregates.Online)
	}
	if all.Aggregates.TotalCPUs != 14 {
		t.Fatalf("expected 14 total CPUs, got %d", all.Aggregates.TotalCPUs)
	}
	if all.Aggregates.TotalRAMBytes != 28*1024*1024*1024 {
		t.Fatalf("unexpected total ram: %d", all.Aggregates.TotalRAMBytes)
	}
	if all.Aggregates.ProbesByOS["linux"] != 2 || all.Aggregates.ProbesByOS["windows"] != 1 {
		t.Fatalf("unexpected os aggregates: %#v", all.Aggregates.ProbesByOS)
	}
	if all.Aggregates.TagDistribution["prod"] != 2 || all.Aggregates.TagDistribution["k8s-host"] != 1 || all.Aggregates.TagDistribution["lab"] != 1 {
		t.Fatalf("unexpected tag distribution: %#v", all.Aggregates.TagDistribution)
	}

	onlineOnly := m.Inventory(InventoryFilter{Status: "ONLINE"})
	if len(onlineOnly.Probes) != 1 || onlineOnly.Probes[0].ID != "probe-1" {
		t.Fatalf("expected probe-1 in online filter, got %#v", onlineOnly.Probes)
	}

	prodOnly := m.Inventory(InventoryFilter{Tag: "PrOd"})
	if len(prodOnly.Probes) != 2 {
		t.Fatalf("expected 2 prod probes, got %d", len(prodOnly.Probes))
	}

	none := m.Inventory(InventoryFilter{Tag: "prod", Status: "offline"})
	if len(none.Probes) != 0 || none.Aggregates.TotalProbes != 0 {
		t.Fatalf("expected no probes for prod+offline, got %#v", none)
	}
}
