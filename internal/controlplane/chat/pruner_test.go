package chat

import (
	"testing"
	"time"
)

func TestPruneOlderThan_RemovesOldMessages(t *testing.T) {
	s, err := NewStore(chatTempDB(t), chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert an "old" message directly into SQLite (bypassing AddMessage so we
	// can set an arbitrary timestamp).
	oldTime := time.Now().UTC().Add(-48 * time.Hour)
	_, err = s.db.Exec(
		`INSERT INTO chat_messages (id, probe_id, role, content, command_id, timestamp) VALUES (?, ?, ?, ?, ?, ?)`,
		"old-msg-1", "probe-1", "user", "old message", "", oldTime.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Mirror the old message in-memory so PruneOlderThan can clean it up there too.
	sess := s.mgr.GetOrCreate("probe-1")
	sess.mu.Lock()
	sess.Messages = append(sess.Messages, Message{
		ID:        "old-msg-1",
		Role:      "user",
		Content:   "old message",
		Timestamp: oldTime,
	})
	sess.mu.Unlock()

	// Add a new message via the normal path (current timestamp).
	s.AddMessage("probe-1", "user", "new message")

	// Prune messages older than 24 hours — should remove only the old one.
	n, err := s.PruneOlderThan(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned message, got %d", n)
	}

	// Verify in-memory: only the new message should remain.
	msgs := s.GetMessages("probe-1", 0)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message remaining in memory, got %d", len(msgs))
	}
	if msgs[0].Content != "new message" {
		t.Fatalf("expected 'new message' to remain, got %q", msgs[0].Content)
	}

	// Verify in SQLite.
	if s.MessageCount() != 1 {
		t.Fatalf("expected 1 message in SQLite, got %d", s.MessageCount())
	}
}

func TestPruneOlderThan_KeepsNewMessages(t *testing.T) {
	s, err := NewStore(chatTempDB(t), chatLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.AddMessage("probe-2", "user", "recent message 1")
	s.AddMessage("probe-2", "assistant", "recent message 2")

	// Prune with a 24h window — nothing should be removed.
	n, err := s.PruneOlderThan(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 pruned, got %d", n)
	}

	msgs := s.GetMessages("probe-2", 0)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages remaining, got %d", len(msgs))
	}
}
