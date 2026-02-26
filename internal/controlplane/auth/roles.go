package auth

// Role describes a user role in the web/UI auth model.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// RolePermissions returns permissions granted to a role.
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
	default:
		return []Permission{}
	}
}

// ValidRole returns true when role is one of the supported user roles.
func ValidRole(role string) bool {
	switch Role(role) {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	default:
		return false
	}
}
