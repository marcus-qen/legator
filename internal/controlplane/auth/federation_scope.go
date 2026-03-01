package auth

import (
	"context"
	"sort"
	"strings"
)

// FederationAccessScope captures tenant/org/scope grants encoded on auth permissions.
//
// Optional grant tokens are parsed from permissions using these additive formats:
//   - tenant:<tenant-id>
//   - org:<org-id>
//   - scope:<scope-id>
//
// Wildcards are supported per-dimension (`tenant:*`, `org:*`, `scope:*`).
// If no grant token is present for a dimension, that dimension is unrestricted.
type FederationAccessScope struct {
	TenantIDs []string
	OrgIDs    []string
	ScopeIDs  []string
}

// FederationAccessScopeFromContext resolves scope grants from the authenticated identity.
func FederationAccessScopeFromContext(ctx context.Context) FederationAccessScope {
	if key := FromContext(ctx); key != nil {
		return FederationAccessScopeFromPermissions(key.Permissions)
	}
	if user := UserFromContext(ctx); user != nil {
		return FederationAccessScopeFromPermissions(user.Permissions)
	}
	return FederationAccessScope{}
}

// FederationAccessScopeFromPermissions parses optional tenant/org/scope grants.
func FederationAccessScopeFromPermissions(perms []Permission) FederationAccessScope {
	if len(perms) == 0 {
		return FederationAccessScope{}
	}

	tenants := map[string]struct{}{}
	orgs := map[string]struct{}{}
	scopes := map[string]struct{}{}
	wildcardTenant := false
	wildcardOrg := false
	wildcardScope := false

	for _, perm := range perms {
		raw := strings.TrimSpace(strings.ToLower(string(perm)))
		if raw == "" {
			continue
		}

		if value, ok := parseFederationGrantValue(raw, "tenant:", "federation:tenant:"); ok {
			if value == "*" {
				wildcardTenant = true
				clear(tenants)
				continue
			}
			if !wildcardTenant {
				tenants[value] = struct{}{}
			}
			continue
		}

		if value, ok := parseFederationGrantValue(raw, "org:", "federation:org:"); ok {
			if value == "*" {
				wildcardOrg = true
				clear(orgs)
				continue
			}
			if !wildcardOrg {
				orgs[value] = struct{}{}
			}
			continue
		}

		if value, ok := parseFederationGrantValue(raw, "scope:", "federation:scope:"); ok {
			if value == "*" {
				wildcardScope = true
				clear(scopes)
				continue
			}
			if !wildcardScope {
				scopes[value] = struct{}{}
			}
		}
	}

	return FederationAccessScope{
		TenantIDs: sortedKeys(tenants),
		OrgIDs:    sortedKeys(orgs),
		ScopeIDs:  sortedKeys(scopes),
	}
}

func parseFederationGrantValue(raw string, prefixes ...string) (string, bool) {
	for _, prefix := range prefixes {
		if !strings.HasPrefix(raw, prefix) {
			continue
		}
		tail := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
		if tail == "" {
			return "", false
		}
		parts := strings.Split(tail, ",")
		if len(parts) == 0 {
			return "", false
		}
		first := strings.TrimSpace(parts[0])
		if first == "" {
			return "", false
		}
		return first, true
	}
	return "", false
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// ContextWithAPIKey is an internal helper for tests/components that need to seed
// authenticated API-key identity into a context.
func ContextWithAPIKey(ctx context.Context, key *APIKey) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, apiKeyContextKey, key)
}

// ContextWithAuthenticatedUser is an internal helper for tests/components that
// need to seed authenticated session user identity into a context.
func ContextWithAuthenticatedUser(ctx context.Context, user *AuthenticatedUser) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, userContextKey, user)
}
