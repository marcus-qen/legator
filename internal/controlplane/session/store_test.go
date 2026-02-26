package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sessions.db")
}

func TestCreateAndValidateToken(t *testing.T) {
	store, err := NewStore(tempDB(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess, err := store.Create("user-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.ID) != 64 {
		t.Fatalf("expected 64-char token, got %d", len(sess.ID))
	}

	validated, err := store.Validate(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.ID != sess.ID {
		t.Fatalf("expected same session id, got %s want %s", validated.ID, sess.ID)
	}
	if validated.UserID != "user-1" {
		t.Fatalf("unexpected user id: %s", validated.UserID)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	store, err := NewStore(tempDB(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess, err := store.Create("user-1")
	if err != nil {
		t.Fatal(err)
	}

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`, past, sess.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Validate(sess.ID); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestDeleteSession(t *testing.T) {
	store, err := NewStore(tempDB(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sess, err := store.Create("user-1")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Validate(sess.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

func TestDeleteByUser(t *testing.T) {
	store, err := NewStore(tempDB(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	s1, err := store.Create("user-1")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := store.Create("user-1")
	if err != nil {
		t.Fatal(err)
	}
	s3, err := store.Create("user-2")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteByUser("user-1"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Validate(s1.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected s1 to be deleted, got %v", err)
	}
	if _, err := store.Validate(s2.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected s2 to be deleted, got %v", err)
	}
	if _, err := store.Validate(s3.ID); err != nil {
		t.Fatalf("expected s3 to remain valid, got %v", err)
	}
}

func TestCleanupRemovesExpired(t *testing.T) {
	store, err := NewStore(tempDB(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	active, err := store.Create("user-1")
	if err != nil {
		t.Fatal(err)
	}
	expired, err := store.Create("user-2")
	if err != nil {
		t.Fatal(err)
	}

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`, past, expired.ID); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.Cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected cleanup to delete 1 session, got %d", deleted)
	}

	if _, err := store.Validate(expired.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected expired session removed, got %v", err)
	}
	if _, err := store.Validate(active.ID); err != nil {
		t.Fatalf("active session should remain valid, got %v", err)
	}
}
