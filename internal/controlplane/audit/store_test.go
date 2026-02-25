package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "audit.db")

	store, err := NewStore(dbPath, 1000)
	if err != nil {
		t.Fatal(err)
	}

	// Record some events
	store.Record(Event{
		Type:    EventProbeRegistered,
		ProbeID: "probe-1",
		Actor:   "system",
		Summary: "Probe registered",
		Detail:  map[string]any{"hostname": "web-01"},
	})
	if err != nil {
		t.Fatal(err)
	}

	store.Record(Event{
		Type:    EventCommandSent,
		ProbeID: "probe-1",
		Actor:   "keith",
		Summary: "Ran ls -la",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Query from memory
	events := store.Query(Filter{ProbeID: "probe-1"})
	if len(events) != 2 {
		t.Fatalf("expected 2 events in memory, got %d", len(events))
	}

	// Count should reflect disk
	if c := store.Count(); c != 2 {
		t.Fatalf("expected 2 persisted events, got %d", c)
	}

	store.Close()

	// Reopen and verify persistence
	store2, err := NewStore(dbPath, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	events = store2.Query(Filter{ProbeID: "probe-1"})
	if len(events) != 2 {
		t.Fatalf("expected 2 events after reopen, got %d", len(events))
	}
}

func TestStoreQueryPersisted(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "audit.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Record(Event{Type: EventCommandSent, ProbeID: "p1", Actor: "a", Summary: "s1"})
	store.Record(Event{Type: EventInventoryUpdate, ProbeID: "p2", Actor: "b", Summary: "s2"})
	store.Record(Event{Type: EventCommandSent, ProbeID: "p1", Actor: "c", Summary: "s3"})

	// Filter by probe_id
	events, err := store.QueryPersisted(Filter{ProbeID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events for p1, got %d", len(events))
	}

	// Filter by type
	events, err = store.QueryPersisted(Filter{Type: EventInventoryUpdate})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 inventory event, got %d", len(events))
	}
}

func TestStoreEmit(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "audit.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Emit(EventProbeRegistered, "p1", "system", "probe registered")

	if store.Count() != 1 {
		t.Fatalf("expected 1 event, got %d", store.Count())
	}
}

func TestStoreSince(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "audit.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Record(Event{Type: EventCommandSent, ProbeID: "p1", Summary: "old"})
	time.Sleep(50 * time.Millisecond)
	cutoff := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)
	store.Record(Event{Type: EventCommandSent, ProbeID: "p1", Summary: "new"})

	events, err := store.QueryPersisted(Filter{Since: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event since cutoff, got %d", len(events))
	}
	if events[0].Summary != "new" {
		t.Fatalf("expected 'new', got %q", events[0].Summary)
	}
}

func TestStoreNonExistentDir(t *testing.T) {
	// Should fail gracefully with a bad path
	_, err := NewStore("/nonexistent/path/audit.db", 100)
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestStoreFilePermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "audit.db")

	store, err := NewStore(dbPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	store.Record(Event{Type: EventCommandSent, Summary: "test"})
	store.Close()

	// File should exist
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file not created")
	}
}
