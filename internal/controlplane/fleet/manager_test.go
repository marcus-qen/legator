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
