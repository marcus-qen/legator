package audit

import (
	"testing"
	"time"
)

func TestFilterWorkspaceID(t *testing.T) {
	log := NewLog(0)

	log.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-a", ProbeID: "p1", Summary: "cmd sent"})
	log.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-b", ProbeID: "p2", Summary: "other cmd"})
	log.Record(Event{Type: EventCommandSent, WorkspaceID: "", ProbeID: "p3", Summary: "legacy no workspace"})

	// Filter by ws-a
	events := log.Query(Filter{WorkspaceID: "ws-a"})
	if len(events) != 1 {
		t.Fatalf("expected 1 event for ws-a, got %d", len(events))
	}
	if events[0].ProbeID != "p1" {
		t.Errorf("expected p1, got %s", events[0].ProbeID)
	}

	// Filter by ws-b
	events = log.Query(Filter{WorkspaceID: "ws-b"})
	if len(events) != 1 || events[0].ProbeID != "p2" {
		t.Fatalf("expected 1 ws-b event, got %+v", events)
	}

	// Empty filter returns all
	events = log.Query(Filter{})
	if len(events) != 3 {
		t.Fatalf("expected 3 events (all), got %d", len(events))
	}
}

func TestStoreWorkspaceFilter(t *testing.T) {
	s, err := NewStore(t.TempDir()+"/audit.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UTC()
	s.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-a", ProbeID: "pa", Actor: "alice", Summary: "a cmd", Timestamp: now})
	s.Record(Event{Type: EventCommandSent, WorkspaceID: "ws-b", ProbeID: "pb", Actor: "bob", Summary: "b cmd", Timestamp: now.Add(-time.Second)})

	// QueryPersisted with workspace filter
	events, err := s.QueryPersisted(Filter{WorkspaceID: "ws-a", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ProbeID != "pa" {
		t.Fatalf("expected 1 ws-a event, got %+v", events)
	}

	// No filter returns both
	all, err := s.QueryPersisted(Filter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 events, got %d", len(all))
	}
}
