package users

import (
	"errors"
	"path/filepath"
	"testing"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "users.db")
}

func TestCreateGetAndGetByUsername(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.Create("alice", "Alice", "secret123", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if created.PasswordHash == "" {
		t.Fatal("expected password hash to be set")
	}
	if created.PasswordHash == "secret123" {
		t.Fatal("password should be hashed")
	}

	byID, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.Username != "alice" {
		t.Fatalf("unexpected username: %s", byID.Username)
	}

	byUsername, err := store.GetByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if byUsername.ID != created.ID {
		t.Fatalf("expected same user ID, got %s want %s", byUsername.ID, created.ID)
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 user, got %d", len(list))
	}
}

func TestDuplicateUsernameRejected(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.Create("alice", "Alice", "secret123", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("alice", "Alice 2", "another-secret", "viewer"); !errors.Is(err, ErrUsernameAlreadyUsed) {
		t.Fatalf("expected duplicate username error, got %v", err)
	}
}

func TestAuthenticateCorrectAndWrongPassword(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.Create("alice", "Alice", "secret123", "admin"); err != nil {
		t.Fatal(err)
	}

	u, err := store.Authenticate("alice", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if u.LastLogin == nil {
		t.Fatal("expected last_login to be set on successful auth")
	}

	if _, err := store.Authenticate("alice", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials error, got %v", err)
	}
}

func TestAuthenticateDisabledUserRejected(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	u, err := store.Create("alice", "Alice", "secret123", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetEnabled(u.ID, false); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Authenticate("alice", "secret123"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("expected user disabled error, got %v", err)
	}
}

func TestUpdatePasswordRoleAndEnabled(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	u, err := store.Create("alice", "Alice", "secret123", "admin")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.UpdatePassword(u.ID, "new-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate("alice", "secret123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password should fail, got %v", err)
	}
	if _, err := store.Authenticate("alice", "new-secret"); err != nil {
		t.Fatalf("new password should succeed, got %v", err)
	}

	if err := store.UpdateRole(u.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != "viewer" {
		t.Fatalf("expected role=viewer, got %s", updated.Role)
	}

	if err := store.SetEnabled(u.ID, false); err != nil {
		t.Fatal(err)
	}
	updated, err = store.Get(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatal("expected user to be disabled")
	}
}

func TestDeleteUser(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	u, err := store.Create("alice", "Alice", "secret123", "admin")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(u.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

func TestCount(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if got := store.Count(); got != 0 {
		t.Fatalf("expected count=0, got %d", got)
	}

	u1, err := store.Create("alice", "Alice", "secret123", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("bob", "Bob", "secret123", "viewer"); err != nil {
		t.Fatal(err)
	}
	if got := store.Count(); got != 2 {
		t.Fatalf("expected count=2, got %d", got)
	}

	if err := store.Delete(u1.ID); err != nil {
		t.Fatal(err)
	}
	if got := store.Count(); got != 1 {
		t.Fatalf("expected count=1, got %d", got)
	}
}

func TestCreateWithIDAndUpdateProfile(t *testing.T) {
	store, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	u, err := store.CreateWithID("oidc:sub-123", "alice", "Alice", "secret123", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "oidc:sub-123" {
		t.Fatalf("expected explicit ID to be used, got %s", u.ID)
	}

	if err := store.UpdateProfile(u.ID, "alice-renamed", "Alice Renamed"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Username != "alice-renamed" || updated.DisplayName != "Alice Renamed" {
		t.Fatalf("unexpected updated profile: %+v", updated)
	}
}
