package server

import (
	"errors"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
)

// withWorkspaceScope is an HTTP middleware that injects the workspace scope
// derived from the request's auth context into the request context for
// downstream handlers (e.g. jobs.Handler).
func (s *Server) withWorkspaceScope(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID := s.workspaceJobFilter(r)
		ctx := jobs.WithWorkspaceScope(r.Context(), wsID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// workspaceScope returns the WorkspaceScope for the current request. When
// workspace isolation is disabled in config (cfg.WorkspaceIsolation == false)
// it always returns an unrestricted scope so every existing single-workspace
// path continues to work unchanged.
func (s *Server) workspaceScope(r *http.Request) auth.WorkspaceScope {
	if !s.cfg.WorkspaceIsolation {
		return auth.WorkspaceScope{Authenticated: true, Restricted: false}
	}
	return auth.WorkspaceScopeFromContext(r.Context())
}

// requireWorkspaceMatch returns true (access granted) when either:
//   - workspace isolation is disabled, OR
//   - the scope is not restricted (wildcard / admin), OR
//   - the resource's workspaceID matches the scope's WorkspaceID, OR
//   - the resource has no workspace tag (legacy row — allow through)
//
// When access is denied it writes 403 and returns false.
func (s *Server) requireWorkspaceMatch(w http.ResponseWriter, scope auth.WorkspaceScope, resourceWorkspaceID string) bool {
	if !scope.Restricted {
		return true // wildcard or isolation disabled
	}
	if resourceWorkspaceID == "" {
		return true // legacy untagged resource — allow through for backwards compat
	}
	if scope.WorkspaceID == resourceWorkspaceID {
		return true
	}
	writeJSONError(w, http.StatusForbidden, "workspace_forbidden", "access to this resource is not permitted for your workspace")
	return false
}

// workspaceJobFilter returns an optional workspace ID to filter job queries.
// Returns empty string when isolation is disabled.
func (s *Server) workspaceJobFilter(r *http.Request) string {
	scope := s.workspaceScope(r)
	if !scope.Restricted {
		return ""
	}
	return scope.WorkspaceID
}

// workspaceCheckErr maps workspace mismatch errors to HTTP 403.
func workspaceCheckErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, jobs.ErrWorkspaceMismatch) {
		writeJSONError(w, http.StatusForbidden, "workspace_forbidden", "access to this resource is not permitted for your workspace")
		return true
	}
	return false
}
