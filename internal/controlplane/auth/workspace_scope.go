package auth

import (
	"context"
	"strings"
)

const (
	workspacePermissionPrefix = "workspace:"
	workspaceWildcardClaim    = "*"
	workspaceAllClaim         = "all"
)

// WorkspaceScope resolves workspace access derived from the auth context.
//
// When Restricted is true, callers must enforce WorkspaceID scoping.
// When Restricted is false and Authenticated is true, the caller is allowed to
// access all workspaces (for example via workspace:* claim).
type WorkspaceScope struct {
	WorkspaceID   string
	Restricted    bool
	Authenticated bool
}

// WorkspaceScopeFromContext resolves workspace access from API key claims or
// session identity.
func WorkspaceScopeFromContext(ctx context.Context) WorkspaceScope {
	if key := FromContext(ctx); key != nil {
		ids, wildcard := workspaceClaimsFromPermissions(key.Permissions)
		if wildcard {
			return WorkspaceScope{Authenticated: true, Restricted: false}
		}
		if len(ids) > 0 {
			return WorkspaceScope{WorkspaceID: ids[0], Authenticated: true, Restricted: true}
		}
		if id := strings.TrimSpace(key.ID); id != "" {
			return WorkspaceScope{WorkspaceID: id, Authenticated: true, Restricted: true}
		}
		return WorkspaceScope{Authenticated: true, Restricted: true}
	}

	if user := UserFromContext(ctx); user != nil {
		ids, wildcard := workspaceClaimsFromPermissions(user.Permissions)
		if wildcard {
			return WorkspaceScope{Authenticated: true, Restricted: false}
		}
		if len(ids) > 0 {
			return WorkspaceScope{WorkspaceID: ids[0], Authenticated: true, Restricted: true}
		}
		if workspaceID := strings.TrimSpace(user.WorkspaceID); workspaceID != "" {
			return WorkspaceScope{WorkspaceID: workspaceID, Authenticated: true, Restricted: true}
		}
		if id := strings.TrimSpace(user.ID); id != "" {
			return WorkspaceScope{WorkspaceID: id, Authenticated: true, Restricted: true}
		}
		return WorkspaceScope{Authenticated: true, Restricted: true}
	}

	return WorkspaceScope{}
}

func workspaceClaimsFromPermissions(perms []Permission) ([]string, bool) {
	ids := make([]string, 0, 1)
	seen := make(map[string]struct{})
	for _, permission := range perms {
		raw := strings.TrimSpace(string(permission))
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, workspacePermissionPrefix) {
			continue
		}
		claim := strings.TrimSpace(strings.TrimPrefix(raw, workspacePermissionPrefix))
		if claim == "" {
			continue
		}
		normalized := strings.ToLower(claim)
		if normalized == workspaceWildcardClaim || normalized == workspaceAllClaim {
			return nil, true
		}
		if _, exists := seen[claim]; exists {
			continue
		}
		seen[claim] = struct{}{}
		ids = append(ids, claim)
	}
	return ids, false
}
