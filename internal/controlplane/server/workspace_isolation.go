package server

import (
    "errors"
    "net/http"
    "strings"

    "github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
)

func normalizeWorkspaceID(workspaceID string) string {
    return strings.ToLower(strings.TrimSpace(workspaceID))
}

func (s *Server) workspaceIsolationEnabled() bool {
    return s != nil && s.cfg.WorkspaceIsolation.Enabled
}

func (s *Server) workspaceScopeForRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
    if !s.workspaceIsolationEnabled() {
        return "", true
    }
    workspaceID, err := auth.WorkspaceIDFromContext(r.Context())
    if err != nil {
        s.writeWorkspaceScopeError(w, err)
        return "", false
    }
    workspaceID = normalizeWorkspaceID(workspaceID)
    return workspaceID, true
}

func (s *Server) workspaceScopeForList(w http.ResponseWriter, r *http.Request) (string, bool) {
    workspaceID, ok := s.workspaceScopeForRequest(w, r)
    if !ok {
        return "", false
    }
    if workspaceID == "*" {
        return "", true
    }
    return workspaceID, true
}

func (s *Server) enforceWorkspaceMatch(w http.ResponseWriter, r *http.Request, resourceWorkspaceID string) bool {
    if !s.workspaceIsolationEnabled() {
        return true
    }
    requestWorkspaceID, ok := s.workspaceScopeForRequest(w, r)
    if !ok {
        return false
    }
    if requestWorkspaceID == "*" {
        return true
    }
    resourceWorkspaceID = normalizeWorkspaceID(resourceWorkspaceID)
    if resourceWorkspaceID == "" || requestWorkspaceID != resourceWorkspaceID {
        writeJSONError(w, http.StatusForbidden, "workspace_forbidden", "workspace scope does not include requested resource")
        return false
    }
    return true
}

func (s *Server) writeWorkspaceScopeError(w http.ResponseWriter, err error) {
    switch {
    case errors.Is(err, auth.ErrWorkspaceScopeAmbiguous):
        writeJSONError(w, http.StatusForbidden, "workspace_scope_ambiguous", "workspace scope is ambiguous")
    default:
        writeJSONError(w, http.StatusForbidden, "workspace_scope_required", "workspace scope is required")
    }
}


func (s *Server) requestWorkspaceID(requestID string) (string, bool) {
    requestID = strings.TrimSpace(requestID)
    if requestID == "" || s == nil || s.asyncJobsManager == nil {
        return "", false
    }
    job, err := s.asyncJobsManager.GetJobByRequestID(requestID)
    if err != nil || job == nil {
        return "", false
    }
    return normalizeWorkspaceID(job.WorkspaceID), true
}

func (s *Server) requestVisibleToWorkspace(requestID, workspaceID string) bool {
    workspaceID = normalizeWorkspaceID(workspaceID)
    if workspaceID == "" {
        return true
    }
    reqWorkspaceID, ok := s.requestWorkspaceID(requestID)
    if !ok {
        return false
    }
    return reqWorkspaceID == workspaceID
}

// withWorkspaceScope is an HTTP middleware that, when workspace isolation is
// enabled, validates and injects the workspace scope into the request context
// before the handler runs. Requests without a valid workspace claim are rejected.
func (s *Server) withWorkspaceScope(next http.HandlerFunc) http.HandlerFunc {
return func(w http.ResponseWriter, r *http.Request) {
if s.workspaceIsolationEnabled() {
workspaceID, ok := s.workspaceScopeForRequest(w, r)
if !ok {
return
}
// Wildcard means admin — pass empty string so handlers show all.
if workspaceID == "*" {
workspaceID = ""
}
r = r.WithContext(jobs.WithWorkspaceScope(r.Context(), workspaceID))
}
next(w, r)
}
}

// workspaceJobFilter returns the workspace ID to use for filtering job/run queries.
// Returns "" when isolation is disabled or caller has wildcard scope.
func (s *Server) workspaceJobFilter(r *http.Request) string {
if !s.workspaceIsolationEnabled() {
return ""
}
workspaceID, err := auth.WorkspaceIDFromContext(r.Context())
if err != nil || workspaceID == "*" {
return ""
}
return normalizeWorkspaceID(workspaceID)
}
