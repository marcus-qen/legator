package chat

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func chatTempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "chat.db")
}

func chatLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestChatStoreAddAndGet(t *testing.T) {
	s, err := NewStore(chatTempDB(t), chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msg := s.AddMessage("probe-1", "user", "hello world")
	if msg == nil {
		t.Fatal("AddMessage returned nil")
	}

	msgs := s.GetMessages("probe-1", 0)
	if len(msgs) != 1 || msgs[0].Content != "hello world" {
		t.Fatalf("unexpected messages: %v", msgs)
	}
}

func TestChatStorePersistsAcrossRestart(t *testing.T) {
	dbPath := chatTempDB(t)

	s1, err := NewStore(dbPath, chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1.AddMessage("probe-1", "user", "first message")
	s1.AddMessage("probe-1", "assistant", "first reply")
	s1.AddMessage("probe-2", "user", "different probe")
	s1.Close()

	// Reopen
	s2, err := NewStore(dbPath, chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p1 := s2.GetMessages("probe-1", 0)
	if len(p1) != 2 {
		t.Fatalf("expected 2 messages for probe-1, got %d", len(p1))
	}
	if p1[0].Content != "first message" || p1[0].Role != "user" {
		t.Fatalf("wrong first message: %+v", p1[0])
	}
	if p1[1].Content != "first reply" || p1[1].Role != "assistant" {
		t.Fatalf("wrong second message: %+v", p1[1])
	}

	p2 := s2.GetMessages("probe-2", 0)
	if len(p2) != 1 || p2[0].Content != "different probe" {
		t.Fatalf("probe-2 messages wrong: %v", p2)
	}
}

func TestChatStoreLimit(t *testing.T) {
	s, err := NewStore(chatTempDB(t), chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 10; i++ {
		s.AddMessage("probe-1", "user", "msg")
	}

	all := s.GetMessages("probe-1", 0)
	if len(all) != 10 {
		t.Fatalf("expected 10, got %d", len(all))
	}

	last3 := s.GetMessages("probe-1", 3)
	if len(last3) != 3 {
		t.Fatalf("expected 3, got %d", len(last3))
	}
}

func TestChatStoreMessageCount(t *testing.T) {
	s, err := NewStore(chatTempDB(t), chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.AddMessage("p1", "user", "a")
	s.AddMessage("p1", "assistant", "b")
	s.AddMessage("p2", "user", "c")

	if s.MessageCount() != 3 {
		t.Fatalf("expected 3, got %d", s.MessageCount())
	}
}

func TestChatStoreDBCreated(t *testing.T) {
	dbPath := chatTempDB(t)

	s, err := NewStore(dbPath, chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("db file not created")
	}
}

func TestChatStoreSubscription(t *testing.T) {
	s, err := NewStore(chatTempDB(t), chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ch, cancel := s.Subscribe("probe-1")
	defer cancel()

	s.AddMessage("probe-1", "user", "hello")

	select {
	case msg := <-ch:
		if msg.Content != "hello" {
			t.Fatalf("unexpected: %s", msg.Content)
		}
	default:
		t.Fatal("expected message on subscription channel")
	}
}
