package auth

import (
	"slices"
	"testing"
)

func TestRolePermissions(t *testing.T) {
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
	if ValidRole("invalid") {
		t.Fatal("invalid should not be a valid role")
	}
}
