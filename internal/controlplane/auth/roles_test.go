package auth

import (
	"slices"
	"testing"
)

func TestRolePermissions(t *testing.T) {
	SetRolePermissionLookup(nil)
	tests := []struct {
		name     string
		role     Role
		expected []Permission
	}{
		{
			name:     "admin",
			role:     RoleAdmin,
			expected: []Permission{PermAdmin},
		},
		{
			name: "operator",
			role: RoleOperator,
			expected: []Permission{
				PermFleetRead,
				PermFleetWrite,
				PermCommandExec,
				PermApprovalRead,
				PermApprovalWrite,
				PermAuditRead,
				PermWebhookManage,
			},
		},
		{
			name: "viewer",
			role: RoleViewer,
			expected: []Permission{
				PermFleetRead,
				PermApprovalRead,
				PermAuditRead,
			},
		},
		{
			name: "auditor",
			role: RoleAuditor,
			expected: []Permission{
				PermFleetRead,
				PermAuditRead,
				PermApprovalRead,
			},
		},
		{
			name:     "invalid role",
			role:     Role("nope"),
			expected: []Permission{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RolePermissions(tt.role)
			if !slices.Equal(got, tt.expected) {
				t.Fatalf("permissions mismatch for %q: got=%v want=%v", tt.role, got, tt.expected)
			}
		})
	}
}

func TestAdminHasPermAdmin(t *testing.T) {
	perms := RolePermissions(RoleAdmin)
	if !slices.Contains(perms, PermAdmin) {
		t.Fatalf("admin role should include %q, got %v", PermAdmin, perms)
	}
}

func TestAuditorRole(t *testing.T) {
	perms := RolePermissions(RoleAuditor)
	must := []Permission{PermFleetRead, PermAuditRead, PermApprovalRead}
	for _, p := range must {
		if !slices.Contains(perms, p) {
			t.Fatalf("auditor role missing permission %q, got %v", p, perms)
		}
	}
	// Auditor should NOT have write or admin permissions
	mustNot := []Permission{PermFleetWrite, PermCommandExec, PermApprovalWrite, PermWebhookManage, PermAdmin}
	for _, p := range mustNot {
		if slices.Contains(perms, p) {
			t.Fatalf("auditor role should not have permission %q", p)
		}
	}
}

func TestValidRole(t *testing.T) {
	if !ValidRole("admin") {
		t.Fatal("admin should be a valid role")
	}
	if !ValidRole("operator") {
		t.Fatal("operator should be a valid role")
	}
	if !ValidRole("viewer") {
		t.Fatal("viewer should be a valid role")
	}
	if !ValidRole("auditor") {
		t.Fatal("auditor should be a valid role")
	}
	if ValidRole("invalid") {
		t.Fatal("invalid should not be a valid role")
	}
}

func TestBuiltInRoles(t *testing.T) {
	roles := BuiltInRoles()
	want := map[Role]bool{
		RoleAdmin: true, RoleOperator: true, RoleViewer: true, RoleAuditor: true,
	}
	for _, r := range roles {
		if !want[r] {
			t.Fatalf("unexpected built-in role: %q", r)
		}
		delete(want, r)
	}
	if len(want) != 0 {
		t.Fatalf("missing built-in roles: %v", want)
	}
}

func TestIsBuiltInRole(t *testing.T) {
	for _, r := range []string{"admin", "operator", "viewer", "auditor"} {
		if !IsBuiltInRole(r) {
			t.Fatalf("%q should be a built-in role", r)
		}
	}
	if IsBuiltInRole("custom-role") {
		t.Fatal("custom-role should not be a built-in role")
	}
}

type stubRoleLookup struct {
	permissions map[string][]Permission
}

func (s stubRoleLookup) GetPermissions(name string) []Permission {
	if s.permissions == nil {
		return nil
	}
	return s.permissions[name]
}

func TestRolePermissions_CustomRoleFallback(t *testing.T) {
	SetRolePermissionLookup(stubRoleLookup{permissions: map[string][]Permission{
		"custom": {PermFleetRead, PermAuditRead},
	}})
	t.Cleanup(func() { SetRolePermissionLookup(nil) })

	got := RolePermissions(Role("custom"))
	want := []Permission{PermFleetRead, PermAuditRead}
	if !slices.Equal(got, want) {
		t.Fatalf("permissions mismatch for custom role: got=%v want=%v", got, want)
	}
}
