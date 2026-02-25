package cmdtracker

import (
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestTrackAndComplete(t *testing.T) {
	tracker := New(30 * time.Second)

	pc := tracker.Track("req-1", "probe-a", "echo hello", protocol.CapObserve)

	if tracker.InFlight() != 1 {
		t.Fatalf("expected 1 in-flight, got %d", tracker.InFlight())
	}

	result := &protocol.CommandResultPayload{
		RequestID: "req-1",
		ExitCode:  0,
		Stdout:    "hello",
		Duration:  42,
	}

	if err := tracker.Complete("req-1", result); err != nil {
		t.Fatalf("complete failed: %v", err)
	}

	// Result should be available on the channel
	select {
	case r := <-pc.Result:
		if r.ExitCode != 0 {
			t.Errorf("expected exit 0, got %d", r.ExitCode)
		}
		if r.Stdout != "hello" {
			t.Errorf("expected 'hello', got %q", r.Stdout)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
	}

	if tracker.InFlight() != 0 {
		t.Fatalf("expected 0 in-flight after complete, got %d", tracker.InFlight())
	}
}

func TestCompleteUnknown(t *testing.T) {
	tracker := New(30 * time.Second)

	err := tracker.Complete("unknown-req", &protocol.CommandResultPayload{})
	if err == nil {
		t.Fatal("expected error for unknown request")
	}
}

func TestCancel(t *testing.T) {
	tracker := New(30 * time.Second)

	tracker.Track("req-cancel", "probe-b", "ls", protocol.CapObserve)
	if tracker.InFlight() != 1 {
		t.Fatalf("expected 1, got %d", tracker.InFlight())
	}

	tracker.Cancel("req-cancel")
	if tracker.InFlight() != 0 {
		t.Fatalf("expected 0 after cancel, got %d", tracker.InFlight())
	}
}

func TestListPending(t *testing.T) {
	tracker := New(30 * time.Second)

	tracker.Track("req-a", "probe-1", "uptime", protocol.CapObserve)
	tracker.Track("req-b", "probe-2", "df -h", protocol.CapDiagnose)

	list := tracker.ListPending()
	if len(list) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(list))
	}

	found := map[string]bool{}
	for _, s := range list {
		found[s.RequestID] = true
	}
	if !found["req-a"] || !found["req-b"] {
		t.Error("missing expected request IDs")
	}
}

func TestReaperExpires(t *testing.T) {
	tracker := &Tracker{
		pending: make(map[string]*PendingCommand),
		ttl:     50 * time.Millisecond,
	}

	pc := &PendingCommand{
		RequestID: "expire-me",
		ProbeID:   "probe-x",
		Command:   "sleep 100",
		Level:     protocol.CapObserve,
		Submitted: time.Now().UTC().Add(-time.Second), // already past TTL
		Result:    make(chan *protocol.CommandResultPayload, 1),
	}

	tracker.mu.Lock()
	tracker.pending["expire-me"] = pc
	tracker.mu.Unlock()

	// Directly call expiry logic
	tracker.expire()

	// Result should be available immediately
	select {
	case r := <-pc.Result:
		if r.ExitCode != -1 {
			t.Errorf("expected exit -1 for timeout, got %d", r.ExitCode)
		}
		if r.Stderr == "" {
			t.Error("expected non-empty stderr for timeout")
		}
	default:
		t.Fatal("expected result to be available after expire()")
	}

	tracker.mu.Lock()
	if len(tracker.pending) != 0 {
		t.Errorf("expected empty pending map after expire, got %d", len(tracker.pending))
	}
	tracker.mu.Unlock()
}
