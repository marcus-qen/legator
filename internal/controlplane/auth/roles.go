package auth

import "sync"

// Role describes a user role in the web/UI auth model.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
	RoleAuditor  Role = "auditor"
)

// RolePermissionLookup resolves permissions for non-built-in roles.
// CustomRoleStore satisfies this interface.
type RolePermissionLookup interface {
	GetPermissions(name string) []Permission
}

var (
	rolePermissionLookupMu sync.RWMutex
	rolePermissionLookup   RolePermissionLookup
)

// SetRolePermissionLookup registers an optional fallback lookup used by
// RolePermissions when a role is not built-in.
func SetRolePermissionLookup(lookup RolePermissionLookup) {
	rolePermissionLookupMu.Lock()
	defer rolePermissionLookupMu.Unlock()
	rolePermissionLookup = lookup
}

// RolePermissions returns permissions granted to a role.
// It resolves built-in roles first, then checks the optional custom role lookup.
func RolePermissions(role Role) []Permission {
	switch role {
	case RoleAdmin:
		return []Permission{PermAdmin}
	case RoleOperator:
		return []Permission{
			PermFleetRead,
			PermFleetWrite,
			PermCommandExec,
			PermApprovalRead,
			PermApprovalWrite,
			PermAuditRead,
			PermWebhookManage,
		}
	case RoleViewer:
		return []Permission{
			PermFleetRead,
			PermApprovalRead,
			PermAuditRead,
		}
	case RoleAuditor:
		return []Permission{
			PermFleetRead,
			PermAuditRead,
			PermApprovalRead,
		}
	default:
		rolePermissionLookupMu.RLock()
		lookup := rolePermissionLookup
		rolePermissionLookupMu.RUnlock()
		if lookup == nil {
			return []Permission{}
		}
		perms := lookup.GetPermissions(string(role))
		if perms == nil {
			return []Permission{}
		}
		// Defensive copy for callers.
		cloned := make([]Permission, len(perms))
		copy(cloned, perms)
		return cloned
	}
}

// BuiltInRoles returns all built-in role names.
func BuiltInRoles() []Role {
	return []Role{RoleAdmin, RoleOperator, RoleViewer, RoleAuditor}
}

// IsBuiltInRole reports whether role is a built-in role.
func IsBuiltInRole(role string) bool {
	switch Role(role) {
	case RoleAdmin, RoleOperator, RoleViewer, RoleAuditor:
		return true
	default:
		return false
	}
}

// ValidRole returns true when role is one of the supported built-in user roles.
func ValidRole(role string) bool {
	return IsBuiltInRole(role)
}
