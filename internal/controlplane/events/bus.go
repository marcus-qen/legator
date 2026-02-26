// Package events provides a pub/sub event bus for fleet-wide events.
// Used by the dashboard for real-time updates and by webhooks for dispatch.
package events

import (
	"encoding/json"
	"sync"
	"time"
)

// EventType classifies fleet events.
type EventType string

const (
	ProbeConnected    EventType = "probe.connected"
	ProbeReconnected  EventType = "probe.reconnected"
	ProbeDisconnected EventType = "probe.disconnected"
	ProbeRegistered   EventType = "probe.registered"
	ProbeOffline      EventType = "probe.offline"
	CommandDispatched EventType = "command.dispatched"
	CommandCompleted  EventType = "command.completed"
	CommandFailed     EventType = "command.failed"
	ApprovalNeeded    EventType = "approval.needed"
	ApprovalDecided   EventType = "approval.decided"
	PolicyChanged     EventType = "policy.changed"
	ChatMessage       EventType = "chat.message"
)

// Event represents a fleet event.
type Event struct {
	Type      EventType   `json:"type"`
	ProbeID   string      `json:"probe_id,omitempty"`
	Summary   string      `json:"summary"`
	Detail    interface{} `json:"detail,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// JSON returns the event as a JSON byte slice.
func (e Event) JSON() []byte {
	data, _ := json.Marshal(e)
	return data
}

// Bus is a simple pub/sub event bus.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[string]chan Event
	bufferSize  int
}

// NewBus creates an event bus.
func NewBus(bufferSize int) *Bus {
	if bufferSize < 1 {
		bufferSize = 64
	}
	return &Bus{
		subscribers: make(map[string]chan Event),
		bufferSize:  bufferSize,
	}
}

// Publish sends an event to all subscribers.
// Non-blocking: drops events for slow subscribers.
func (b *Bus) Publish(evt Event) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		select {
		case ch <- evt:
		default:
			// Drop for slow subscriber — better than blocking
		}
	}
}

// Subscribe returns a channel of events. Call Unsubscribe with the returned id when done.
func (b *Bus) Subscribe(id string) <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, b.bufferSize)
	b.subscribers[id] = ch
	return ch
}

// Unsubscribe removes a subscriber.
func (b *Bus) Unsubscribe(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if ch, ok := b.subscribers[id]; ok {
		close(ch)
		delete(b.subscribers, id)
	}
}

// SubscriberCount returns the number of active subscribers.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
