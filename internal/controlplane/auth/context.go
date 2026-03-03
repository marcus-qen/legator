package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
)

const workspaceGrantPrefix = "workspace:"

var (
	ErrWorkspaceScopeMissing   = errors.New("workspace scope missing")
	ErrWorkspaceScopeAmbiguous = errors.New("workspace scope ambiguous")
)

// WorkspaceIDFromContext resolves a workspace identifier from the authenticated
// user/api-key permission grants. Supported grants:
//   - workspace:<workspace-id>
//   - workspace:*
//
// When a wildcard is present, "*" is returned. When multiple concrete
// workspaces are present, ErrWorkspaceScopeAmbiguous is returned.
func WorkspaceIDFromContext(ctx context.Context) (string, error) {
	if key := FromContext(ctx); key != nil {
		return WorkspaceIDFromPermissions(key.Permissions)
	}
	if user := UserFromContext(ctx); user != nil {
		return WorkspaceIDFromPermissions(user.Permissions)
	}
	return "", ErrWorkspaceScopeMissing
}

// WorkspaceIDFromPermissions parses workspace grant tokens from permissions.
func WorkspaceIDFromPermissions(perms []Permission) (string, error) {
	if len(perms) == 0 {
		return "", ErrWorkspaceScopeMissing
	}

	workspaceSet := make(map[string]struct{})
	wildcard := false

	for _, perm := range perms {
		raw := strings.TrimSpace(string(perm))
		if raw == "" {
			continue
		}
		if strings.EqualFold(raw, string(PermAdmin)) {
			return "*", nil
		}
		if !strings.HasPrefix(strings.ToLower(raw), workspaceGrantPrefix) {
			continue
		}
		value := strings.TrimSpace(raw[len(workspaceGrantPrefix):])
		if value == "" {
			continue
		}
		if value == "*" {
			wildcard = true
			break
		}
		workspaceSet[strings.ToLower(value)] = struct{}{}
	}

	if wildcard {
		return "*", nil
	}
	if len(workspaceSet) == 0 {
		return "", ErrWorkspaceScopeMissing
	}
	if len(workspaceSet) > 1 {
		return "", ErrWorkspaceScopeAmbiguous
	}
	ids := make([]string, 0, len(workspaceSet))
	for id := range workspaceSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids[0], nil
}
