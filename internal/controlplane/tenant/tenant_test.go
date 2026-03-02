package tenant

import (
	"context"
	"path/filepath"
	"testing"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "tenant.db")
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ── Tenant CRUD ──────────────────────────────────────────────────────────────

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)

	tenant, err := s.Create("Acme Corp", "acme-corp", "ops@acme.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tenant.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if tenant.Slug != "acme-corp" {
		t.Fatalf("expected slug acme-corp, got %q", tenant.Slug)
	}

	got, err := s.Get(tenant.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Acme Corp" {
		t.Fatalf("expected name Acme Corp, got %q", got.Name)
	}
	if got.ContactEmail != "ops@acme.com" {
		t.Fatalf("expected email ops@acme.com, got %q", got.ContactEmail)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("nonexistent")
	if err != ErrTenantNotFound {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestSlugConflict(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("Acme", "acme", ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.Create("Acme 2", "acme", "")
	if err != ErrSlugConflict {
		t.Fatalf("expected ErrSlugConflict, got %v", err)
	}
}

func TestNormalizeSlugOnCreate(t *testing.T) {
	s := newTestStore(t)
	t1, err := s.Create("My Company", "My Company", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if t1.Slug != "my-company" {
		t.Fatalf("expected slug my-company, got %q", t1.Slug)
	}
}

func TestGetBySlug(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create("Beta Inc", "beta-inc", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetBySlug("beta-inc")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("ID mismatch: %q vs %q", got.ID, created.ID)
	}
}

func TestList(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("Alpha", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("Beta", "beta", ""); err != nil {
		t.Fatal(err)
	}
	tenants, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(tenants))
	}
}

func TestListEmpty(t *testing.T) {
	s := newTestStore(t)
	tenants, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if tenants == nil {
		t.Fatal("expected non-nil slice for empty list")
	}
}

func TestUpdate(t *testing.T) {
	s := newTestStore(t)
	t1, err := s.Create("Old Name", "old-name", "old@example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := s.Update(t1.ID, "New Name", "new@example.com")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "New Name" {
		t.Fatalf("expected New Name, got %q", updated.Name)
	}
	if updated.ContactEmail != "new@example.com" {
		t.Fatalf("expected new@example.com, got %q", updated.ContactEmail)
	}
	if updated.Slug != "old-name" {
		t.Fatalf("slug must not change, got %q", updated.Slug)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update("nonexistent", "Name", "")
	if err != ErrTenantNotFound {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	t1, err := s.Create("Acme", "acme", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Delete(t1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.Get(t1.ID)
	if err != ErrTenantNotFound {
		t.Fatalf("expected ErrTenantNotFound after delete, got %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("nonexistent"); err != ErrTenantNotFound {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ── User-Tenant membership ───────────────────────────────────────────────────

func TestSetAndGetUserTenants(t *testing.T) {
	s := newTestStore(t)
	t1, _ := s.Create("Alpha", "alpha", "")
	t2, _ := s.Create("Beta", "beta", "")

	if err := s.SetUserTenants("user-1", []string{t1.ID, t2.ID}); err != nil {
		t.Fatalf("SetUserTenants: %v", err)
	}

	ids, err := s.GetUserTenants("user-1")
	if err != nil {
		t.Fatalf("GetUserTenants: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 tenant IDs, got %d", len(ids))
	}
}

func TestSetUserTenantsReplaces(t *testing.T) {
	s := newTestStore(t)
	t1, _ := s.Create("Alpha", "alpha", "")
	t2, _ := s.Create("Beta", "beta", "")

	_ = s.SetUserTenants("user-1", []string{t1.ID, t2.ID})
	_ = s.SetUserTenants("user-1", []string{t1.ID}) // replace with single tenant

	ids, err := s.GetUserTenants("user-1")
	if err != nil {
		t.Fatalf("GetUserTenants: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 tenant ID after replace, got %d", len(ids))
	}
	if ids[0] != t1.ID {
		t.Fatalf("expected %q, got %q", t1.ID, ids[0])
	}
}

func TestDeleteCleansUpMemberships(t *testing.T) {
	s := newTestStore(t)
	t1, _ := s.Create("Temp", "temp", "")
	_ = s.SetUserTenants("user-1", []string{t1.ID})

	_ = s.Delete(t1.ID)

	ids, err := s.GetUserTenants("user-1")
	if err != nil {
		t.Fatalf("GetUserTenants after tenant delete: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty memberships after tenant delete, got %v", ids)
	}
}

func TestGetUserTenantsEmpty(t *testing.T) {
	s := newTestStore(t)
	ids, err := s.GetUserTenants("nobody")
	if err != nil {
		t.Fatalf("GetUserTenants for unknown user: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty, got %v", ids)
	}
}

// ── Scope helpers ────────────────────────────────────────────────────────────

func TestScopeAllowsTenant(t *testing.T) {
	scope := Scope{TenantIDs: []string{"t1", "t2"}}
	if !scope.AllowsTenant("t1") {
		t.Fatal("expected t1 to be allowed")
	}
	if scope.AllowsTenant("t3") {
		t.Fatal("expected t3 to be denied")
	}
}

func TestScopeAdminAllowsAll(t *testing.T) {
	scope := Scope{IsAdmin: true}
	if !scope.AllowsTenant("any-tenant") {
		t.Fatal("admin scope should allow any tenant")
	}
	if !scope.AllowsTenant("") {
		t.Fatal("admin scope should allow empty-tenant probes")
	}
}

func TestScopeNonAdminDeniesEmptyTenant(t *testing.T) {
	scope := Scope{TenantIDs: []string{"t1"}}
	if scope.AllowsTenant("") {
		t.Fatal("non-admin scope should deny empty tenant probes")
	}
}

func TestScopeContextRoundTrip(t *testing.T) {
	ctx := WithScope(context.Background(), Scope{IsAdmin: true, TenantIDs: []string{"t1"}})
	got := ScopeFromContext(ctx)
	if !got.IsAdmin {
		t.Fatal("expected IsAdmin to round-trip")
	}
	if len(got.TenantIDs) != 1 || got.TenantIDs[0] != "t1" {
		t.Fatalf("unexpected TenantIDs: %v", got.TenantIDs)
	}
}

func TestScopeFromContextZeroWhenUnset(t *testing.T) {
	got := ScopeFromContext(context.Background())
	if got.IsAdmin {
		t.Fatal("unset context should yield zero scope")
	}
	if len(got.TenantIDs) != 0 {
		t.Fatal("unset context should yield empty TenantIDs")
	}
}
