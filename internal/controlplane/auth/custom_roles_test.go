package auth

import (
	"errors"
	"path/filepath"
	"testing"
)

func tempRolesDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "roles.db")
}

func TestCustomRoleCRUD(t *testing.T) {
	store, err := NewCustomRoleStore(tempRolesDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Create
	perms := []Permission{PermFleetRead, PermAuditRead}
	cr, err := store.Create("security-reviewer", perms, "Read-only security reviewer")
	if err != nil {
		t.Fatalf("create custom role: %v", err)
	}
	if cr.Name != "security-reviewer" {
		t.Fatalf("unexpected name: %s", cr.Name)
	}
	if len(cr.Permissions) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(cr.Permissions))
	}

	// Get
	got, err := store.Get("security-reviewer")
	if err != nil {
		t.Fatalf("get custom role: %v", err)
	}
	if got.Description != "Read-only security reviewer" {
		t.Fatalf("unexpected description: %s", got.Description)
	}

	// List
	list, err := store.List()
	if err != nil {
		t.Fatalf("list custom roles: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 custom role, got %d", len(list))
	}

	// Update
	newPerms := []Permission{PermFleetRead, PermAuditRead, PermApprovalRead}
	updated, err := store.Update("security-reviewer", newPerms, "Updated description")
	if err != nil {
		t.Fatalf("update custom role: %v", err)
	}
	if len(updated.Permissions) != 3 {
		t.Fatalf("expected 3 permissions after update, got %d", len(updated.Permissions))
	}
	if updated.Description != "Updated description" {
		t.Fatalf("unexpected description: %s", updated.Description)
	}

	// Delete
	if err := store.Delete("security-reviewer"); err != nil {
		t.Fatalf("delete custom role: %v", err)
	}

	// Get after delete should fail
	_, err = store.Get("security-reviewer")
	if !errors.Is(err, ErrCustomRoleNotFound) {
		t.Fatalf("expected ErrCustomRoleNotFound, got %v", err)
	}

	// List after delete should be empty
	list, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 custom roles, got %d", len(list))
	}
}

func TestCustomRoleDuplicateName(t *testing.T) {
	store, err := NewCustomRoleStore(tempRolesDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.Create("myrole", nil, ""); err != nil {
		t.Fatal(err)
	}
	_, err = store.Create("myrole", nil, "")
	if !errors.Is(err, ErrCustomRoleExists) {
		t.Fatalf("expected ErrCustomRoleExists, got %v", err)
	}
}

func TestCustomRoleBuiltInProtection(t *testing.T) {
	store, err := NewCustomRoleStore(tempRolesDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Cannot create custom role with built-in name
	for _, name := range []string{"admin", "operator", "viewer", "auditor"} {
		_, err := store.Create(name, nil, "")
		if !errors.Is(err, ErrBuiltInRole) {
			t.Fatalf("expected ErrBuiltInRole for %q, got %v", name, err)
		}
	}

	// Cannot delete built-in role
	if err := store.Delete("admin"); !errors.Is(err, ErrBuiltInRole) {
		t.Fatalf("expected ErrBuiltInRole when deleting admin, got %v", err)
	}
}

func TestCustomRoleGetPermissions(t *testing.T) {
	store, err := NewCustomRoleStore(tempRolesDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// GetPermissions on unknown role returns nil
	if got := store.GetPermissions("unknown"); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}

	perms := []Permission{PermFleetRead}
	if _, err := store.Create("my-role", perms, ""); err != nil {
		t.Fatal(err)
	}

	got := store.GetPermissions("my-role")
	if len(got) != 1 || got[0] != PermFleetRead {
		t.Fatalf("unexpected permissions: %v", got)
	}
}

func TestCustomRolePermissionsResolveCorrectly(t *testing.T) {
	store, err := NewCustomRoleStore(tempRolesDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	perms := []Permission{PermFleetRead, PermApprovalRead}
	if _, err := store.Create("restricted", perms, "Restricted user"); err != nil {
		t.Fatal(err)
	}

	// Verify GetPermissions round-trips correctly
	got := store.GetPermissions("restricted")
	if len(got) != 2 {
		t.Fatalf("expected 2 permissions, got %d: %v", len(got), got)
	}
}
