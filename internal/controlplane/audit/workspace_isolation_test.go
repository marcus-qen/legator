package audit

import (
"path/filepath"
"testing"
)

func newTestStore(t *testing.T) *Store {
t.Helper()
s, err := NewStore(filepath.Join(t.TempDir(), "audit.db"), 100)
if err != nil {
t.Fatalf("new store: %v", err)
}
t.Cleanup(func() { _ = s.Close() })
return s
}

func TestAuditStoreWorkspaceIsolation(t *testing.T) {
s := newTestStore(t)

s.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-a", Actor: "alice", Summary: "cmd from A"})
s.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-b", Actor: "bob", Summary: "cmd from B"})
s.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-a", Actor: "alice", Summary: "cmd from A again"})

// Query workspace-a only.
got, err := s.QueryPersisted(Filter{WorkspaceID: "ws-a"})
if err != nil {
t.Fatalf("query: %v", err)
}
if len(got) != 2 {
t.Fatalf("expected 2 events for ws-a, got %d", len(got))
}
for _, e := range got {
if e.WorkspaceID != "ws-a" {
t.Fatalf("unexpected workspace in result: %s", e.WorkspaceID)
}
}
}

func TestAuditFilterNilWorkspaceReturnsAll(t *testing.T) {
s := newTestStore(t)

s.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-a", Summary: "a"})
s.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-b", Summary: "b"})

got, err := s.QueryPersisted(Filter{})
if err != nil {
t.Fatalf("query: %v", err)
}
if len(got) != 2 {
t.Fatalf("expected 2 events with no workspace filter, got %d", len(got))
}
}

func TestAuditWorkspaceIDPersistedCorrectly(t *testing.T) {
s := newTestStore(t)
s.Record(Event{Type: EventCommandSent, WorkspaceID: "myws", Summary: "test"})

got, err := s.QueryPersisted(Filter{WorkspaceID: "myws"})
if err != nil {
t.Fatalf("query: %v", err)
}
if len(got) != 1 {
t.Fatalf("expected 1 event, got %d", len(got))
}
if got[0].WorkspaceID != "myws" {
t.Fatalf("workspace_id not persisted: got %q want %q", got[0].WorkspaceID, "myws")
}
}
