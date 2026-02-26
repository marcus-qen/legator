package events

import (
	"testing"
	"time"
)

func TestPublishAndSubscribe(t *testing.T) {
	bus := NewBus(16)
	ch := bus.Subscribe("test-1")

	bus.Publish(Event{
		Type:    ProbeConnected,
		ProbeID: "prb-1",
		Summary: "probe connected",
	})

	select {
	case evt := <-ch:
		if evt.Type != ProbeConnected {
			t.Fatalf("expected ProbeConnected, got %s", evt.Type)
		}
		if evt.ProbeID != "prb-1" {
			t.Fatalf("expected prb-1, got %s", evt.ProbeID)
		}
		if evt.Timestamp.IsZero() {
			t.Fatal("timestamp should be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	bus.Unsubscribe("test-1")
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewBus(16)
	ch1 := bus.Subscribe("s1")
	ch2 := bus.Subscribe("s2")

	bus.Publish(Event{Type: ProbeOffline, Summary: "test"})

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Type != ProbeOffline {
				t.Fatalf("wrong type: %s", evt.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}

	if bus.SubscriberCount() != 2 {
		t.Fatalf("expected 2 subscribers, got %d", bus.SubscriberCount())
	}

	bus.Unsubscribe("s1")
	bus.Unsubscribe("s2")

	if bus.SubscriberCount() != 0 {
		t.Fatalf("expected 0 subscribers, got %d", bus.SubscriberCount())
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := NewBus(1) // tiny buffer
	_ = bus.Subscribe("slow")

	// Publish more events than the buffer can hold — should not block
	for i := 0; i < 100; i++ {
		bus.Publish(Event{Type: CommandDispatched, Summary: "test"})
	}
	// If we get here, it didn't block
}

func TestEventJSON(t *testing.T) {
	evt := Event{
		Type:      ProbeRegistered,
		ProbeID:   "prb-test",
		Summary:   "new probe",
		Timestamp: time.Now(),
	}
	data := evt.JSON()
	if len(data) == 0 {
		t.Fatal("empty JSON")
	}
}
