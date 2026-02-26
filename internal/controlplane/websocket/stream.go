// Package websocket - output streaming support for the hub.
package websocket

import (
	"sync"

	"github.com/marcus-qen/legator/internal/protocol"
)

// StreamSubscriber receives output chunks for a specific request.
type StreamSubscriber struct {
	RequestID string
	Ch        chan protocol.OutputChunkPayload
	done      chan struct{}
	once      sync.Once
}

// Close stops the subscriber.
func (s *StreamSubscriber) Close() {
	s.once.Do(func() {
		close(s.done)
	})
}

// streamRegistry manages subscribers waiting for streaming output.
type streamRegistry struct {
	subs map[string][]*StreamSubscriber // keyed by requestID
	mu   sync.RWMutex
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{
		subs: make(map[string][]*StreamSubscriber),
	}
}

// Subscribe creates a subscriber for a request's output chunks.
// Returns the subscriber and a cleanup function.
func (sr *streamRegistry) Subscribe(requestID string, bufSize int) (*StreamSubscriber, func()) {
	sub := &StreamSubscriber{
		RequestID: requestID,
		Ch:        make(chan protocol.OutputChunkPayload, bufSize),
		done:      make(chan struct{}),
	}

	sr.mu.Lock()
	sr.subs[requestID] = append(sr.subs[requestID], sub)
	sr.mu.Unlock()

	cleanup := func() {
		sub.Close()
		sr.mu.Lock()
		defer sr.mu.Unlock()
		subs := sr.subs[requestID]
		for i, s := range subs {
			if s == sub {
				sr.subs[requestID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(sr.subs[requestID]) == 0 {
			delete(sr.subs, requestID)
		}
	}

	return sub, cleanup
}

// Dispatch sends an output chunk to all subscribers for that request.
func (sr *streamRegistry) Dispatch(chunk protocol.OutputChunkPayload) {
	sr.mu.RLock()
	subs := sr.subs[chunk.RequestID]
	sr.mu.RUnlock()

	for _, sub := range subs {
		select {
		case <-sub.done:
			// subscriber cancelled
		case sub.Ch <- chunk:
			// delivered
		default:
			// channel full, drop (subscriber too slow)
		}
	}
}
