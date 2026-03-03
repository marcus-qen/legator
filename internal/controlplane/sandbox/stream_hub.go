package sandbox

import (
	"sync"
)

const (
	// hubChannelBuffer is the number of chunks buffered per subscriber channel.
	// When the buffer is full the oldest item is dropped rather than blocking.
	hubChannelBuffer = 256
)

// subscriber holds a single WebSocket client's receive channel.
type subscriber struct {
	ch   chan *OutputChunk
	done chan struct{}
	once sync.Once
}

// close signals that this subscriber is done. Safe to call multiple times.
func (s *subscriber) close() {
	s.once.Do(func() {
		close(s.done)
	})
}

// StreamHub is an in-memory fan-out hub that distributes output chunks to
// WebSocket subscribers grouped by sandbox ID. It is goroutine-safe.
type StreamHub struct {
	mu   sync.RWMutex
	subs map[string][]*subscriber // sandboxID → subscribers
}

// NewStreamHub allocates and returns a ready-to-use StreamHub.
func NewStreamHub() *StreamHub {
	return &StreamHub{
		subs: make(map[string][]*subscriber),
	}
}

// Subscribe registers a new subscriber for the given sandbox and returns the
// receive channel plus a cleanup function that must be called when the
// subscriber is done (e.g. on WebSocket close).
func (h *StreamHub) Subscribe(sandboxID string) (<-chan *OutputChunk, func()) {
	sub := &subscriber{
		ch:   make(chan *OutputChunk, hubChannelBuffer),
		done: make(chan struct{}),
	}

	h.mu.Lock()
	h.subs[sandboxID] = append(h.subs[sandboxID], sub)
	h.mu.Unlock()

	cleanup := func() {
		sub.close()
		h.mu.Lock()
		defer h.mu.Unlock()
		subs := h.subs[sandboxID]
		for i, s := range subs {
			if s == sub {
				h.subs[sandboxID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(h.subs[sandboxID]) == 0 {
			delete(h.subs, sandboxID)
		}
	}

	return sub.ch, cleanup
}

// Broadcast delivers a chunk to all current subscribers for the sandbox.
// If a subscriber's buffer is full the oldest item is dropped (non-blocking).
func (h *StreamHub) Broadcast(chunk *OutputChunk) {
	if chunk == nil {
		return
	}

	h.mu.RLock()
	subs := h.subs[chunk.SandboxID]
	// Copy the slice so we don't hold the lock while writing.
	snapshot := make([]*subscriber, len(subs))
	copy(snapshot, subs)
	h.mu.RUnlock()

	for _, sub := range snapshot {
		select {
		case <-sub.done:
			// subscriber is shutting down, skip
		case sub.ch <- chunk:
			// delivered
		default:
			// buffer full — drop oldest and push newest
			select {
			case <-sub.ch: // drain one
			default:
			}
			select {
			case sub.ch <- chunk:
			default:
			}
		}
	}
}

// Evict closes all subscribers for a sandbox (called on destroy) and removes
// them from the registry.
func (h *StreamHub) Evict(sandboxID string) {
	h.mu.Lock()
	subs := h.subs[sandboxID]
	delete(h.subs, sandboxID)
	h.mu.Unlock()

	for _, sub := range subs {
		sub.close()
	}
}

// Close shuts down the hub, evicting all subscribers.
func (h *StreamHub) Close() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.subs))
	for id := range h.subs {
		ids = append(ids, id)
	}
	h.mu.Unlock()

	for _, id := range ids {
		h.Evict(id)
	}
}

// SubscriberCount returns the number of active subscribers for a sandbox.
// Primarily useful for testing.
func (h *StreamHub) SubscriberCount(sandboxID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[sandboxID])
}
