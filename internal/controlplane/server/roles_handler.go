package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/users"
)

// ── Role info types ──────────────────────────────────────────

type roleInfo struct {
	Permissions []string `json:"permissions"`
	BuiltIn     bool     `json:"built_in"`
	Description string   `json:"description,omitempty"`
}

// ── Permission Matrix ────────────────────────────────────────

// handlePermissionMatrix is a public endpoint that returns the full
// permission matrix: all roles (built-in + custom) and all known permissions.
// No authentication required.
func (s *Server) handlePermissionMatrix(w http.ResponseWriter, r *http.Request) {
	roles := make(map[string]roleInfo)

	// Built-in roles
	for _, role := range auth.BuiltInRoles() {
		perms := auth.RolePermissions(role)
		permStrs := make([]string, len(perms))
		for i, p := range perms {
			permStrs[i] = string(p)
		}
		roles[string(role)] = roleInfo{
			Permissions: permStrs,
			BuiltIn:     true,
		}
	}

	// Custom roles
	if s.customRoleStore != nil {
		customRoles, err := s.customRoleStore.List()
		if err == nil {
			for _, cr := range customRoles {
				permStrs := make([]string, len(cr.Permissions))
				for i, p := range cr.Permissions {
					permStrs[i] = string(p)
				}
				roles[cr.Name] = roleInfo{
					Permissions: permStrs,
					BuiltIn:     false,
					Description: cr.Description,
				}
			}
		}
	}

	// All known permissions
	allPerms := []string{
		string(auth.PermAdmin),
		string(auth.PermFleetRead),
		string(auth.PermFleetWrite),
		string(auth.PermCommandExec),
		string(auth.PermApprovalRead),
		string(auth.PermApprovalWrite),
		string(auth.PermAuditRead),
		string(auth.PermWebhookManage),
	}

	// Also collect any custom permissions that appear in custom roles
	if s.customRoleStore != nil {
		customRoles, _ := s.customRoleStore.List()
		seen := make(map[string]bool)
		for _, p := range allPerms {
			seen[p] = true
		}
		for _, cr := range customRoles {
			for _, p := range cr.Permissions {
				ps := string(p)
				if !seen[ps] {
					allPerms = append(allPerms, ps)
					seen[ps] = true
				}
			}
		}
	}
	sort.Strings(allPerms)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"roles":       roles,
		"permissions": allPerms,
	})
}

// ── Role CRUD ────────────────────────────────────────────────

// handleListRoles lists all roles: built-in and custom.
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	type roleEntry struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		BuiltIn     bool     `json:"built_in"`
		Description string   `json:"description,omitempty"`
	}

	var roles []roleEntry

	// Built-in roles
	for _, role := range auth.BuiltInRoles() {
		perms := auth.RolePermissions(role)
		permStrs := make([]string, len(perms))
		for i, p := range perms {
			permStrs[i] = string(p)
		}
		roles = append(roles, roleEntry{
			Name:        string(role),
			Permissions: permStrs,
			BuiltIn:     true,
		})
	}

	// Custom roles
	if s.customRoleStore != nil {
		customRoles, err := s.customRoleStore.List()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		for _, cr := range customRoles {
			permStrs := make([]string, len(cr.Permissions))
			for i, p := range cr.Permissions {
				permStrs[i] = string(p)
			}
			roles = append(roles, roleEntry{
				Name:        cr.Name,
				Permissions: permStrs,
				BuiltIn:     false,
				Description: cr.Description,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"roles": roles,
		"total": len(roles),
	})
}

// handleCreateRole creates a new custom role (admin only).
func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	if s.customRoleStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "custom roles not enabled")
		return
	}

	var body struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
		Description string   `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if body.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name required")
		return
	}

	perms := make([]auth.Permission, len(body.Permissions))
	for i, p := range body.Permissions {
		perms[i] = auth.Permission(p)
	}

	cr, err := s.customRoleStore.Create(body.Name, perms, body.Description)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrBuiltInRole):
			writeJSONError(w, http.StatusConflict, "conflict", "cannot create custom role with built-in role name")
		case errors.Is(err, auth.ErrCustomRoleExists):
			writeJSONError(w, http.StatusConflict, "conflict", "role already exists")
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(cr)
}

// handleDeleteRole deletes a custom role (admin only, cannot delete built-in).
func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	if s.customRoleStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "custom roles not enabled")
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "role name required")
		return
	}

	if err := s.customRoleStore.Delete(name); err != nil {
		switch {
		case errors.Is(err, auth.ErrBuiltInRole):
			writeJSONError(w, http.StatusForbidden, "forbidden", "cannot delete a built-in role")
		case errors.Is(err, auth.ErrCustomRoleNotFound):
			writeJSONError(w, http.StatusNotFound, "not_found", "role not found")
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "name": name})
}

// ── User Role Assignment ─────────────────────────────────────

// handleGetUserRole returns a user's current role.
func (s *Server) handleGetUserRole(w http.ResponseWriter, r *http.Request) {
	if s.userStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not enabled")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "user id required")
		return
	}

	u, err := s.userStore.Get(id)
	if err != nil {
		if errors.Is(err, users.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"user_id": u.ID,
		"role":    u.Role,
	})
}

// handlePutUserRole assigns a role to a user (admin only).
func (s *Server) handlePutUserRole(w http.ResponseWriter, r *http.Request) {
	if s.userStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not enabled")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "user id required")
		return
	}

	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if body.Role == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "role required")
		return
	}

	// Validate role: must be built-in or a known custom role
	if !auth.IsBuiltInRole(body.Role) {
		if s.customRoleStore == nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "unknown role: "+body.Role)
			return
		}
		_, err := s.customRoleStore.Get(body.Role)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "unknown role: "+body.Role)
			return
		}
	}

	if err := s.userStore.UpdateRole(id, body.Role); err != nil {
		if errors.Is(err, users.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"user_id": id,
		"role":    body.Role,
		"status":  "updated",
	})
}
