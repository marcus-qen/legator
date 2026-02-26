package fleet

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "fleet.db")
}

func TestStoreRegisterAndGet(t *testing.T) {
	s, err := NewStore(tempDBPath(t), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ps := s.Register("p1", "web-01", "linux", "amd64")
	if ps.ID != "p1" {
		t.Fatalf("expected p1, got %s", ps.ID)
	}

	got, ok := s.Get("p1")
	if !ok || got.Hostname != "web-01" {
		t.Fatal("Get after Register failed")
	}
}

func TestStoreListAndCount(t *testing.T) {
	s, err := NewStore(tempDBPath(t), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Register("p1", "web-01", "linux", "amd64")
	s.Register("p2", "db-01", "linux", "arm64")

	if len(s.List()) != 2 {
		t.Fatalf("expected 2, got %d", len(s.List()))
	}
	counts := s.Count()
	if counts["online"] != 2 {
		t.Fatalf("expected 2 online, got %v", counts)
	}
}

func TestStorePersistsAcrossRestart(t *testing.T) {
	dbPath := tempDBPath(t)

	// First instance: register probes
	s1, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1.Register("p1", "web-01", "linux", "amd64")
	s1.Register("p2", "db-01", "linux", "arm64")
	_ = s1.SetPolicy("p1", protocol.CapRemediate)
	_ = s1.SetTags("p2", []string{"production", "database"})
	s1.Close()

	// Second instance: should recover state
	s2, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	list := s2.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 probes after restart, got %d", len(list))
	}

	p1, ok := s2.Get("p1")
	if !ok {
		t.Fatal("p1 not found after restart")
	}
	if p1.PolicyLevel != protocol.CapRemediate {
		t.Fatalf("expected remediate, got %s", p1.PolicyLevel)
	}

	p2, ok := s2.Get("p2")
	if !ok {
		t.Fatal("p2 not found after restart")
	}
	if len(p2.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %v", p2.Tags)
	}
	tagSet := map[string]bool{}
	for _, tag := range p2.Tags {
		tagSet[tag] = true
	}
	if !tagSet["production"] || !tagSet["database"] {
		t.Fatalf("expected production+database tags, got %v", p2.Tags)
	}
}

func TestStoreHeartbeatPersists(t *testing.T) {
	dbPath := tempDBPath(t)

	s1, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1.Register("p1", "web-01", "linux", "amd64")

	// Backdate last_seen
	s1.mgr.mu.Lock()
	s1.mgr.probes["p1"].LastSeen = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s1.mgr.mu.Unlock()
	_ = s1.upsertProbe(s1.mgr.probes["p1"])

	// Heartbeat updates it
	_ = s1.Heartbeat("p1", &protocol.HeartbeatPayload{ProbeID: "p1"})
	s1.Close()

	// Reload and check
	s2, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p1, _ := s2.Get("p1")
	if p1.LastSeen.Year() == 2026 && p1.LastSeen.Month() == 1 {
		t.Fatal("heartbeat did not persist updated last_seen")
	}
}

func TestStoreInventoryPersists(t *testing.T) {
	dbPath := tempDBPath(t)

	s1, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1.Register("p1", "web-01", "linux", "amd64")

	inv := &protocol.InventoryPayload{
		ProbeID:  "p1",
		Hostname: "web-01",
		OS:       "linux",
		Arch:     "amd64",
		Kernel:   "6.1.0",
		CPUs:     4,
		MemTotal: 8 * 1024 * 1024 * 1024,
	}
	_ = s1.UpdateInventory("p1", inv)
	s1.Close()

	s2, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p1, _ := s2.Get("p1")
	if p1.Inventory == nil {
		t.Fatal("inventory not persisted")
	}
	if p1.Inventory.Kernel != "6.1.0" || p1.Inventory.CPUs != 4 {
		t.Fatalf("inventory mismatch: %+v", p1.Inventory)
	}
}

func TestStoreMarkOfflinePersists(t *testing.T) {
	dbPath := tempDBPath(t)

	s1, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1.Register("p1", "web-01", "linux", "amd64")

	// Backdate last_seen so it goes offline
	s1.mgr.mu.Lock()
	s1.mgr.probes["p1"].LastSeen = time.Now().UTC().Add(-5 * time.Minute)
	s1.mgr.mu.Unlock()
	_ = s1.upsertProbe(s1.mgr.probes["p1"])

	s1.MarkOffline(60 * time.Second)
	s1.Close()

	s2, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p1, _ := s2.Get("p1")
	if p1.Status != "offline" {
		t.Fatalf("expected offline, got %s", p1.Status)
	}
}

func TestStoreSetOnlinePersists(t *testing.T) {
	dbPath := tempDBPath(t)

	s1, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1.Register("p1", "web-01", "linux", "amd64")

	oldSeen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s1.mgr.mu.Lock()
	s1.mgr.probes["p1"].Status = "offline"
	s1.mgr.probes["p1"].LastSeen = oldSeen
	s1.mgr.mu.Unlock()
	_ = s1.upsertProbe(s1.mgr.probes["p1"])

	if err := s1.SetOnline("p1"); err != nil {
		t.Fatalf("set online failed: %v", err)
	}

	s1.Close()

	s2, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p1, ok := s2.Get("p1")
	if !ok {
		t.Fatal("expected p1 after reopen")
	}
	if p1.Status != "online" {
		t.Fatalf("expected online, got %s", p1.Status)
	}
	if !p1.LastSeen.After(oldSeen) {
		t.Fatalf("expected last_seen to be updated, got %s", p1.LastSeen)
	}

	if err := s2.SetOnline("missing"); err == nil {
		t.Fatal("expected error for unknown probe")
	}
}

func TestStoreDBFileCreated(t *testing.T) {
	dbPath := tempDBPath(t)

	s, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file was not created")
	}
}

func TestStoreSetAPIKeyPersists(t *testing.T) {
	dbPath := tempDBPath(t)

	s1, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1.Register("p1", "web-01", "linux", "amd64")
	if err := s1.SetAPIKey("p1", "lgk_rotated_key"); err != nil {
		t.Fatalf("set api key: %v", err)
	}
	s1.Close()

	s2, err := NewStore(dbPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p1, ok := s2.Get("p1")
	if !ok {
		t.Fatal("p1 not found after reopen")
	}
	if p1.APIKey != "lgk_rotated_key" {
		t.Fatalf("expected persisted api key, got %q", p1.APIKey)
	}
}
