package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
)

type federationScopeAuthorizationError struct {
	dimension string
	requested string
	allowed   []string
}

func (e *federationScopeAuthorizationError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.allowed) == 0 {
		return fmt.Sprintf("requested %s %q is not permitted", e.dimension, e.requested)
	}
	return fmt.Sprintf("requested %s %q is not permitted (allowed: %s)", e.dimension, e.requested, strings.Join(e.allowed, ","))
}

func applyFederationAccessFilter(filter fleet.FederationFilter, access auth.FederationAccessScope) (fleet.FederationFilter, *federationScopeAuthorizationError) {
	filter.TenantID = normalizeFederationScopeValue(filter.TenantID)
	filter.OrgID = normalizeFederationScopeValue(filter.OrgID)
	filter.ScopeID = normalizeFederationScopeValue(filter.ScopeID)

	filter.AllowedTenantIDs = normalizeFederationScopeValues(access.TenantIDs)
	filter.AllowedOrgIDs = normalizeFederationScopeValues(access.OrgIDs)
	filter.AllowedScopeIDs = normalizeFederationScopeValues(access.ScopeIDs)

	if err := validateFederationScopeDimension("tenant", filter.TenantID, filter.AllowedTenantIDs); err != nil {
		return filter, err
	}
	if err := validateFederationScopeDimension("org", filter.OrgID, filter.AllowedOrgIDs); err != nil {
		return filter, err
	}
	if err := validateFederationScopeDimension("scope", filter.ScopeID, filter.AllowedScopeIDs); err != nil {
		return filter, err
	}

	return filter, nil
}

func validateFederationScopeDimension(dimension, requested string, allowed []string) *federationScopeAuthorizationError {
	if requested == "" || len(allowed) == 0 {
		return nil
	}
	if containsFederationScopeValue(allowed, requested) {
		return nil
	}
	return &federationScopeAuthorizationError{
		dimension: dimension,
		requested: requested,
		allowed:   append([]string(nil), allowed...),
	}
}

func normalizeFederationScopeValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeFederationScopeValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		norm := normalizeFederationScopeValue(value)
		if norm == "" {
			continue
		}
		seen[norm] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsFederationScopeValue(values []string, target string) bool {
	normTarget := normalizeFederationScopeValue(target)
	for _, value := range values {
		if normalizeFederationScopeValue(value) == normTarget {
			return true
		}
	}
	return false
}

func firstNonEmptyFederationQueryParam(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func actorFromAuthContext(ctx context.Context) string {
	if user := auth.UserFromContext(ctx); user != nil {
		if user.Username != "" {
			return user.Username
		}
		if user.ID != "" {
			return user.ID
		}
	}
	if key := auth.FromContext(ctx); key != nil {
		if key.Name != "" {
			return key.Name
		}
		if key.ID != "" {
			return key.ID
		}
	}
	return "anonymous"
}

func (s *Server) recordFederationAuthorizationDenied(r *http.Request, perm auth.Permission, requested fleet.FederationFilter, effective fleet.FederationFilter, access auth.FederationAccessScope, authzErr *federationScopeAuthorizationError) {
	if s == nil || s.auditStore == nil || r == nil {
		return
	}

	reason := "forbidden_scope"
	if authzErr != nil {
		reason = authzErr.Error()
	}

	detail := map[string]any{
		"method":                r.Method,
		"path":                  r.URL.Path,
		"required_permission":   string(perm),
		"reason":                reason,
		"requested_tenant_id":   requested.TenantID,
		"requested_org_id":      requested.OrgID,
		"requested_scope_id":    requested.ScopeID,
		"effective_tenant_id":   effective.TenantID,
		"effective_org_id":      effective.OrgID,
		"effective_scope_id":    effective.ScopeID,
		"allowed_tenant_ids":    append([]string(nil), access.TenantIDs...),
		"allowed_org_ids":       append([]string(nil), access.OrgIDs...),
		"allowed_scope_ids":     append([]string(nil), access.ScopeIDs...),
	}

	s.recordAudit(audit.Event{
		Timestamp: time.Now().UTC(),
		Type:      audit.EventAuthorizationDenied,
		Actor:     actorFromAuthContext(r.Context()),
		Summary:   fmt.Sprintf("federation authorization denied for %s %s", r.Method, r.URL.Path),
		Detail:    detail,
	})
}

func (s *Server) recordFederationReadAudit(r *http.Request, surface string, requested fleet.FederationFilter, effective fleet.FederationFilter, access auth.FederationAccessScope, sourceCount int, probeCount int) {
	if s == nil || s.auditStore == nil || r == nil {
		return
	}

	detail := map[string]any{
		"surface":               surface,
		"path":                  r.URL.Path,
		"requested_tenant_id":   requested.TenantID,
		"requested_org_id":      requested.OrgID,
		"requested_scope_id":    requested.ScopeID,
		"effective_tenant_id":   effective.TenantID,
		"effective_org_id":      effective.OrgID,
		"effective_scope_id":    effective.ScopeID,
		"allowed_tenant_ids":    append([]string(nil), access.TenantIDs...),
		"allowed_org_ids":       append([]string(nil), access.OrgIDs...),
		"allowed_scope_ids":     append([]string(nil), access.ScopeIDs...),
		"sources":               sourceCount,
		"probes":                probeCount,
	}

	s.recordAudit(audit.Event{
		Timestamp: time.Now().UTC(),
		Type:      audit.EventFederationRead,
		Actor:     actorFromAuthContext(r.Context()),
		Summary:   fmt.Sprintf("federation read on %s", surface),
		Detail:    detail,
	})
}
