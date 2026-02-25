package audit

import (
	"testing"
	"time"
)

func TestRecordAndQuery(t *testing.T) {
	log := NewLog(0)

	log.Emit(EventProbeRegistered, "prb-001", "system", "Probe prb-001 registered")
	log.Emit(EventCommandSent, "prb-001", "keith", "echo hello")
	log.Emit(EventCommandResult, "prb-001", "prb-001", "exit 0")
	log.Emit(EventPolicyChanged, "prb-002", "keith", "observe → diagnose")

	if log.Count() != 4 {
		t.Errorf("expected 4 events, got %d", log.Count())
	}

	// Query by probe
	events := log.Query(Filter{ProbeID: "prb-001"})
	if len(events) != 3 {
		t.Errorf("expected 3 events for prb-001, got %d", len(events))
	}

	// Query by type
	events = log.Query(Filter{Type: EventCommandSent})
	if len(events) != 1 {
		t.Errorf("expected 1 command.sent event, got %d", len(events))
	}

	// Recent
	events = log.Recent(2)
	if len(events) != 2 {
		t.Errorf("expected 2 recent events, got %d", len(events))
	}
	if events[0].Type != EventPolicyChanged {
		t.Errorf("expected newest first, got %s", events[0].Type)
	}
}

func TestRingBuffer(t *testing.T) {
	log := NewLog(3)

	for i := 0; i < 5; i++ {
		log.Emit(EventCommandSent, "prb-001", "system", "cmd")
	}

	if log.Count() != 3 {
		t.Errorf("ring buffer should cap at 3, got %d", log.Count())
	}
}

func TestQuerySince(t *testing.T) {
	log := NewLog(0)

	log.Record(Event{
		Type:      EventProbeRegistered,
		Timestamp: time.Now().UTC().Add(-2 * time.Hour),
		Summary:   "old event",
	})
	log.Record(Event{
		Type:      EventCommandSent,
		Timestamp: time.Now().UTC().Add(-30 * time.Minute),
		Summary:   "recent event",
	})

	events := log.Query(Filter{Since: time.Now().UTC().Add(-1 * time.Hour)})
	if len(events) != 1 {
		t.Errorf("expected 1 event since last hour, got %d", len(events))
	}
}

func TestBeforeAfterState(t *testing.T) {
	log := NewLog(0)

	log.Record(Event{
		Type:    EventPolicyChanged,
		ProbeID: "prb-001",
		Actor:   "keith",
		Summary: "Policy level change",
		Before:  map[string]string{"level": "observe"},
		After:   map[string]string{"level": "remediate"},
	})

	events := log.Recent(1)
	if events[0].Before == nil || events[0].After == nil {
		t.Error("before/after state should be preserved")
	}
}
