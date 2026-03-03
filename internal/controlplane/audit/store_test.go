package audit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestStoreQueryPersistedCursorPagination(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "audit.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Now().UTC().Add(-time.Hour)
	for i := 1; i <= 5; i++ {
		store.Record(Event{
			ID:        fmt.Sprintf("evt-%d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Type:      EventCommandSent,
			ProbeID:   "probe-cursor",
			Summary:   fmt.Sprintf("event-%d", i),
		})
	}

	page1, err := store.QueryPersisted(Filter{ProbeID: "probe-cursor", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 {
		t.Fatalf("expected first page size 2, got %d", len(page1))
	}
	if page1[0].ID != "evt-5" || page1[1].ID != "evt-4" {
		t.Fatalf("unexpected first page IDs: %s, %s", page1[0].ID, page1[1].ID)
	}

	page2, err := store.QueryPersisted(Filter{ProbeID: "probe-cursor", Cursor: page1[1].ID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Fatalf("expected second page size 2, got %d", len(page2))
	}
	if page2[0].ID != "evt-3" || page2[1].ID != "evt-2" {
		t.Fatalf("unexpected second page IDs: %s, %s", page2[0].ID, page2[1].ID)
	}
}

func TestStorePurge(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "audit.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	store.Record(Event{ID: "old-1", Timestamp: now.Add(-72 * time.Hour), Type: EventCommandSent, Summary: "old-1"})
	store.Record(Event{ID: "old-2", Timestamp: now.Add(-48 * time.Hour), Type: EventCommandSent, Summary: "old-2"})
	store.Record(Event{ID: "new-1", Timestamp: now.Add(-1 * time.Hour), Type: EventCommandSent, Summary: "new-1"})

	deleted, err := store.Purge(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 deleted rows, got %d", deleted)
	}

	events, err := store.QueryPersisted(Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event after purge, got %d", len(events))
	}
	if events[0].ID != "new-1" {
		t.Fatalf("expected remaining event new-1, got %s", events[0].ID)
	}
}

func TestStoreChainModeWritesHashes(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithOptions(filepath.Join(dir, "audit.db"), 100, StoreOptions{
		ChainMode: true,
		ChainKey:  strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Record(Event{ID: "evt-chain-1", Type: EventCommandSent, ProbeID: "p1", Actor: "api", Summary: "first"})
	store.Record(Event{ID: "evt-chain-2", Type: EventCommandResult, ProbeID: "p1", Actor: "api", Summary: "second"})

	events, err := store.QueryPersisted(Filter{ProbeID: "p1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	newest := events[0]
	oldest := events[1]
	if oldest.PrevHash != GenesisHash {
		t.Fatalf("expected oldest prev hash to be genesis, got %q", oldest.PrevHash)
	}
	if oldest.EntryHash == "" {
		t.Fatal("expected oldest entry hash to be set")
	}
	if newest.PrevHash != oldest.EntryHash {
		t.Fatalf("expected newest prev hash to link to previous entry hash; got %q want %q", newest.PrevHash, oldest.EntryHash)
	}
	if newest.EntryHash == "" {
		t.Fatal("expected newest entry hash to be set")
	}
}

func TestStoreVerifyChainDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithOptions(filepath.Join(dir, "audit.db"), 100, StoreOptions{
		ChainMode: true,
		ChainKey:  strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Record(Event{ID: "tamper-1", Type: EventCommandSent, ProbeID: "p1", Actor: "api", Summary: "safe-1"})
	store.Record(Event{ID: "tamper-2", Type: EventCommandResult, ProbeID: "p1", Actor: "api", Summary: "safe-2"})

	before, err := store.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("verify before tamper: %v", err)
	}
	if !before.Valid || before.EntriesChecked != 2 || before.FirstInvalidAt != nil {
		t.Fatalf("expected valid chain before tamper, got %+v", before)
	}

	if _, err := store.db.Exec(`UPDATE audit_events SET summary = ? WHERE id = ?`, "tampered", "tamper-1"); err != nil {
		t.Fatalf("tamper row: %v", err)
	}

	after, err := store.VerifyChain(context.Background())
	if err != nil {
		t.Fatalf("verify after tamper: %v", err)
	}
	if after.Valid {
		t.Fatalf("expected invalid chain after tamper, got %+v", after)
	}
	if after.FirstInvalidAt == nil {
		t.Fatalf("expected first_invalid_at to be set after tamper, got %+v", after)
	}
}

func TestStoreStreamCSVIncludesChainColumnsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithOptions(filepath.Join(dir, "audit.db"), 100, StoreOptions{
		ChainMode: true,
		ChainKey:  strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Record(Event{ID: "csv-chain-1", Type: EventCommandSent, Summary: "csv"})

	var buf strings.Builder
	if err := store.StreamCSV(context.Background(), &buf, Filter{}); err != nil {
		t.Fatalf("stream csv: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "prev_hash") || !strings.Contains(out, "entry_hash") {
		t.Fatalf("expected chain columns in csv output, got: %s", out)
	}
}

func TestStoreChainModeDisabledKeepsHashesEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "audit.db"), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Record(Event{ID: "unsigned-1", Type: EventCommandSent, Summary: "unsigned"})
	events, err := store.QueryPersisted(Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].PrevHash != "" || events[0].EntryHash != "" {
		t.Fatalf("expected empty hashes when chain mode disabled, got prev=%q entry=%q", events[0].PrevHash, events[0].EntryHash)
	}
}
