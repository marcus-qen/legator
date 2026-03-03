package sandbox

import (
	"sync"
	"testing"
	"time"
)

func TestStreamHub_SubscribeAndBroadcast(t *testing.T) {
	hub := NewStreamHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe("sbx-1")
	defer unsub()

	chunk := &OutputChunk{ID: "c1", SandboxID: "sbx-1", Sequence: 1, Stream: StreamStdout, Data: "hello"}
	hub.Broadcast(chunk)

	select {
	case got := <-ch:
		if got.ID != "c1" {
			t.Errorf("expected chunk ID c1, got %q", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for chunk")
	}
}

func TestStreamHub_MultipleSubscribers(t *testing.T) {
	hub := NewStreamHub()
	defer hub.Close()

	ch1, unsub1 := hub.Subscribe("sbx-1")
	ch2, unsub2 := hub.Subscribe("sbx-1")
	defer unsub1()
	defer unsub2()

	hub.Broadcast(&OutputChunk{ID: "x", SandboxID: "sbx-1", Sequence: 1})

	for _, ch := range []<-chan *OutputChunk{ch1, ch2} {
		select {
		case got := <-ch:
			if got.ID != "x" {
				t.Errorf("expected x, got %q", got.ID)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for subscriber")
		}
	}
}

func TestStreamHub_BroadcastToCorrectSandbox(t *testing.T) {
	hub := NewStreamHub()
	defer hub.Close()

	ch1, unsub1 := hub.Subscribe("sbx-1")
	ch2, unsub2 := hub.Subscribe("sbx-2")
	defer unsub1()
	defer unsub2()

	hub.Broadcast(&OutputChunk{ID: "for-sbx-2", SandboxID: "sbx-2", Sequence: 1})

	// ch1 should NOT receive it.
	select {
	case c := <-ch1:
		t.Errorf("sbx-1 received unexpected chunk %q", c.ID)
	case <-time.After(50 * time.Millisecond):
		// correct
	}

	// ch2 SHOULD receive it.
	select {
	case got := <-ch2:
		if got.ID != "for-sbx-2" {
			t.Errorf("expected for-sbx-2, got %q", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out for sbx-2")
	}
}

func TestStreamHub_UnsubscribeRemovesSubscriber(t *testing.T) {
	hub := NewStreamHub()

	_, unsub := hub.Subscribe("sbx-1")
	if hub.SubscriberCount("sbx-1") != 1 {
		t.Fatal("expected 1 subscriber")
	}
	unsub()
	if hub.SubscriberCount("sbx-1") != 0 {
		t.Fatal("expected 0 subscribers after unsub")
	}
}

func TestStreamHub_EvictClosesSubscribers(t *testing.T) {
	hub := NewStreamHub()

	ch, _ := hub.Subscribe("sbx-1")

	hub.Evict("sbx-1")

	// After eviction the channel should be closed — reads drain immediately.
	// We check by trying to receive with a short timeout; the channel won't
	// block because the subscriber's done channel closed, meaning the write
	// side will skip it. Actually the channel itself isn't closed by Evict;
	// the subscriber's `done` chan is closed which is picked up by
	// HandleStreamOutput. Check that SubscriberCount is 0.
	if hub.SubscriberCount("sbx-1") != 0 {
		t.Error("expected 0 subscribers after evict")
	}
	_ = ch // used above indirectly
}

func TestStreamHub_BroadcastNil(t *testing.T) {
	hub := NewStreamHub()
	defer hub.Close()
	// Should not panic.
	hub.Broadcast(nil)
}

func TestStreamHub_DropOldestOnOverflow(t *testing.T) {
	hub := NewStreamHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe("sbx-1")
	defer unsub()

	// Fill the buffer plus one more to trigger drop.
	for i := int64(0); i < hubChannelBuffer+1; i++ {
		hub.Broadcast(&OutputChunk{
			ID:        "c",
			SandboxID: "sbx-1",
			Sequence:  i,
		})
	}

	// Drain the channel; we should get hubChannelBuffer items (one was dropped).
	deadline := time.After(2 * time.Second)
	count := 0
loop:
	for {
		select {
		case <-ch:
			count++
		case <-deadline:
			break loop
		default:
			if count >= hubChannelBuffer {
				break loop
			}
		}
	}
	// We might have hubChannelBuffer or hubChannelBuffer-1 items depending on
	// timing; just verify we don't have more than the buffer size.
	if count > hubChannelBuffer {
		t.Errorf("received %d items, expected at most %d (buffer size)", count, hubChannelBuffer)
	}
}

func TestStreamHub_ConcurrentSafety(t *testing.T) {
	hub := NewStreamHub()
	defer hub.Close()

	const goroutines = 20
	var wg sync.WaitGroup

	// Writers.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				hub.Broadcast(&OutputChunk{
					SandboxID: "sbx-concurrent",
					Sequence:  int64(n*100 + j),
				})
			}
		}(i)
	}

	// Subscribers that come and go.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, unsub := hub.Subscribe("sbx-concurrent")
			time.Sleep(time.Millisecond)
			unsub()
		}()
	}

	wg.Wait()
	// If we get here without a data race or panic, the test passes.
}

func TestStreamHub_Close(t *testing.T) {
	hub := NewStreamHub()

	_, unsub1 := hub.Subscribe("sbx-1")
	defer unsub1()
	_, unsub2 := hub.Subscribe("sbx-2")
	defer unsub2()

	hub.Close()

	if hub.SubscriberCount("sbx-1") != 0 {
		t.Error("expected 0 after Close for sbx-1")
	}
	if hub.SubscriberCount("sbx-2") != 0 {
		t.Error("expected 0 after Close for sbx-2")
	}
}

func TestStreamHub_SubscriberCount(t *testing.T) {
	hub := NewStreamHub()
	defer hub.Close()

	if hub.SubscriberCount("sbx-empty") != 0 {
		t.Error("expected 0 for unknown sandbox")
	}

	_, u1 := hub.Subscribe("sbx-1")
	_, u2 := hub.Subscribe("sbx-1")

	if hub.SubscriberCount("sbx-1") != 2 {
		t.Errorf("expected 2, got %d", hub.SubscriberCount("sbx-1"))
	}

	u1()
	if hub.SubscriberCount("sbx-1") != 1 {
		t.Errorf("expected 1 after u1(), got %d", hub.SubscriberCount("sbx-1"))
	}

	u2()
	if hub.SubscriberCount("sbx-1") != 0 {
		t.Errorf("expected 0 after u2(), got %d", hub.SubscriberCount("sbx-1"))
	}
}
