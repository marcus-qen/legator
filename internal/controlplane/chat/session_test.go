package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestGetOrCreateReturnsSameSession(t *testing.T) {
	m := NewManager(testLogger())

	s1 := m.GetOrCreate("probe-1")
	s2 := m.GetOrCreate("probe-1")

	if s1 == nil || s2 == nil {
		t.Fatal("expected non-nil sessions")
	}
	if s1 != s2 {
		t.Fatalf("expected same session pointer, got different values")
	}
	if s1.ProbeID != "probe-1" {
		t.Fatalf("expected probe id probe-1, got %q", s1.ProbeID)
	}
}

func TestAddMessageAppendsToHistory(t *testing.T) {
	m := NewManager(testLogger())

	nilMsg := m.AddMessage("probe-2", "user", "hello")
	if nilMsg == nil {
		t.Fatal("expected message")
	}
	m.AddMessage("probe-2", "assistant", "world")

	msgs := m.GetMessages("probe-2", 0)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "hello" || msgs[1].Content != "world" {
		t.Fatalf("unexpected history order: %#v", msgs)
	}
	if msgs[0].ID == msgs[1].ID {
		t.Fatal("message IDs must be unique")
	}
}

func TestGetMessagesReturnsLastN(t *testing.T) {
	m := NewManager(testLogger())

	for i := 0; i < 5; i++ {
		m.AddMessage("probe-3", "user", "msg-"+string(rune('a'+i)))
	}
	messages := m.GetMessages("probe-3", 3)
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[0].Content != "msg-c" || messages[2].Content != "msg-e" {
		t.Fatalf("expected last 3 messages, got %#v", messages)
	}
}

func TestSubscribeDeliversMessages(t *testing.T) {
	m := NewManager(testLogger())
	messages, cancel := m.Subscribe("probe-4")
	defer cancel()

	m.AddMessage("probe-4", "assistant", "hello from probe")

	select {
	case msg := <-messages:
		if msg.Content != "hello from probe" {
			t.Fatalf("unexpected message content: %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestSubscribeMultipleSubscribers(t *testing.T) {
	m := NewManager(testLogger())
	first, cancelFirst := m.Subscribe("probe-5")
	second, cancelSecond := m.Subscribe("probe-5")
	defer cancelFirst()
	defer cancelSecond()

	m.AddMessage("probe-5", "system", "hello everyone")

	select {
	case msg := <-first:
		if msg.Content != "hello everyone" {
			t.Fatalf("first subscriber expected message content, got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first subscriber")
	}

	select {
	case msg := <-second:
		if msg.Content != "hello everyone" {
			t.Fatalf("second subscriber expected message content, got %q", msg.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second subscriber")
	}
}

func TestSubscribeCancelStopsDelivery(t *testing.T) {
	m := NewManager(testLogger())
	messages, cancel := m.Subscribe("probe-6")
	cancel()

	m.AddMessage("probe-6", "assistant", "should not arrive")

	select {
	case _, ok := <-messages:
		if ok {
			t.Fatal("received message after cancel")
		}
	case <-time.After(200 * time.Millisecond):
		// expected: channel should close and not receive any message
	}
}

func TestRespond_ReturnsFriendlyMessageWhenResponderFails(t *testing.T) {
	m := NewManager(testLogger())
	m.SetResponder(func(probeID, userMessage string, history []Message) (string, error) {
		return "", errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
	})

	reply := m.respond("probe-llm", "hello")
	if reply != llmUnavailableUserMessage {
		t.Fatalf("expected friendly fallback message, got %q", reply)
	}
	if strings.Contains(reply, "connection refused") {
		t.Fatalf("reply leaked raw backend error: %q", reply)
	}
}
