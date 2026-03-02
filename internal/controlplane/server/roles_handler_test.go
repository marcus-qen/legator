package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	"go.uber.org/zap"
)

// newRolesTestServer creates a test server with auth and custom roles enabled.
func newRolesTestServer(t *testing.T) *Server {
	t.Helper()

	t.Setenv("LEGATOR_AUTH", "true")
	t.Setenv("LEGATOR_LLM_PROVIDER", "")
	t.Setenv("LEGATOR_SIGNING_KEY", strings.Repeat("a", 64))

	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = t.TempDir()
	cfg.AuthEnabled = true

	srv, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// newCustomRoleStore opens a fresh custom role store for testing.
func newCustomRoleStore(t *testing.T) *auth.CustomRoleStore {
	t.Helper()
	store, err := auth.NewCustomRoleStore(filepath.Join(t.TempDir(), "roles.db"))
	if err != nil {
		t.Fatalf("new custom role store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestPermissionMatrixEndpoint verifies the public /api/v1/auth/permissions endpoint.
func TestPermissionMatrixEndpoint(t *testing.T) {
	srv := newRolesTestServer(t)
	rr := makeRequest(t, srv, http.MethodGet, "/api/v1/auth/permissions", "", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var result struct {
		Roles map[string]struct {
			Permissions []string `json:"permissions"`
			BuiltIn     bool     `json:"built_in"`
		} `json:"roles"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// All 4 built-in roles must be present
	for _, name := range []string{"admin", "operator", "viewer", "auditor"} {
		role, ok := result.Roles[name]
		if !ok {
			t.Fatalf("missing built-in role %q from permission matrix", name)
		}
		if !role.BuiltIn {
			t.Fatalf("role %q should be built_in=true", name)
		}
		if len(role.Permissions) == 0 {
			t.Fatalf("role %q has no permissions", name)
		}
	}

	// permissions array must be non-empty
	if len(result.Permissions) == 0 {
		t.Fatal("permissions array is empty")
	}
}

// TestPermissionMatrixIsPublic verifies no auth is needed for the permission matrix endpoint.
func TestPermissionMatrixIsPublic(t *testing.T) {
	srv := newRolesTestServer(t)
	// Make request without any token
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/permissions", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 without auth, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestListRolesEndpoint verifies GET /api/v1/roles returns all roles.
func TestListRolesEndpoint(t *testing.T) {
	srv := newRolesTestServer(t)
	key := createAPIKey(t, srv, "admin-key", auth.PermAdmin)

	rr := makeRequest(t, srv, http.MethodGet, "/api/v1/roles", key, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var result struct {
		Roles []struct {
			Name    string `json:"name"`
			BuiltIn bool   `json:"built_in"`
		} `json:"roles"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.Total < 4 {
		t.Fatalf("expected at least 4 built-in roles, got %d", result.Total)
	}

	names := make(map[string]bool)
	for _, r := range result.Roles {
		names[r.Name] = true
	}
	for _, want := range []string{"admin", "operator", "viewer", "auditor"} {
		if !names[want] {
			t.Fatalf("missing role %q from list", want)
		}
	}
}

// TestCustomRoleCRUDViaAPI verifies the full custom role lifecycle via API.
func TestCustomRoleCRUDViaAPI(t *testing.T) {
	srv := newRolesTestServer(t)
	if srv.customRoleStore == nil {
		t.Skip("custom role store not initialized")
	}

	key := createAPIKey(t, srv, "admin-key", auth.PermAdmin)

	// Create custom role
	body := `{"name":"security-ops","permissions":["fleet:read","audit:read"],"description":"Security ops team"}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/roles", key, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", rr.Code, rr.Body.String())
	}

	var created struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	if created.Name != "security-ops" {
		t.Fatalf("unexpected name: %s", created.Name)
	}

	// List should include custom role
	rr = makeRequest(t, srv, http.MethodGet, "/api/v1/roles", key, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rr.Code)
	}

	var listResult struct {
		Roles []struct {
			Name    string `json:"name"`
			BuiltIn bool   `json:"built_in"`
		} `json:"roles"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &listResult)

	found := false
	for _, r := range listResult.Roles {
		if r.Name == "security-ops" {
			found = true
			if r.BuiltIn {
				t.Fatal("security-ops should not be built_in")
			}
		}
	}
	if !found {
		t.Fatal("security-ops not found in role list")
	}

	// Delete the custom role
	rr = makeRequest(t, srv, http.MethodDelete, "/api/v1/roles/security-ops", key, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestCannotDeleteBuiltInRole verifies built-in roles are protected.
func TestCannotDeleteBuiltInRole(t *testing.T) {
	srv := newRolesTestServer(t)
	key := createAPIKey(t, srv, "admin-key", auth.PermAdmin)

	for _, name := range []string{"admin", "operator", "viewer", "auditor"} {
		rr := makeRequest(t, srv, http.MethodDelete, "/api/v1/roles/"+name, key, "")
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for deleting built-in role %q, got %d body=%s", name, rr.Code, rr.Body.String())
		}
	}
}

// TestUserRoleAssignmentAPI verifies GET and PUT /api/v1/users/{id}/role.
func TestUserRoleAssignmentAPI(t *testing.T) {
	srv := newRolesTestServer(t)
	if srv.userStore == nil {
		t.Skip("user store not initialized")
	}

	key := createAPIKey(t, srv, "admin-key", auth.PermAdmin)

	// Create a test user
	userBody := `{"username":"testuser","display_name":"Test User","password":"pass123","role":"viewer"}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/users", key, userBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create user: expected 201, got %d body=%s", rr.Code, rr.Body.String())
	}

	var user struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &user); err != nil {
		t.Fatalf("unmarshal user: %v", err)
	}

	// GET current role
	rr = makeRequest(t, srv, http.MethodGet, "/api/v1/users/"+user.ID+"/role", key, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get role: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var roleResp struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &roleResp)
	if roleResp.Role != "viewer" {
		t.Fatalf("expected viewer role, got %s", roleResp.Role)
	}

	// PUT to change role to operator
	rr = makeRequest(t, srv, http.MethodPut, "/api/v1/users/"+user.ID+"/role", key, `{"role":"operator"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("put role: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	// GET again to verify change
	rr = makeRequest(t, srv, http.MethodGet, "/api/v1/users/"+user.ID+"/role", key, "")
	_ = json.Unmarshal(rr.Body.Bytes(), &roleResp)
	if roleResp.Role != "operator" {
		t.Fatalf("expected operator role after update, got %s", roleResp.Role)
	}
}

// TestAuditorRoleAssignment verifies the auditor built-in role can be assigned.
func TestAuditorRoleAssignment(t *testing.T) {
	srv := newRolesTestServer(t)
	if srv.userStore == nil {
		t.Skip("user store not initialized")
	}

	key := createAPIKey(t, srv, "admin-key", auth.PermAdmin)

	// Create user
	userBody := `{"username":"auditor-test","display_name":"Auditor","password":"pass123","role":"viewer"}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/users", key, userBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var user struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &user)

	// Assign auditor role
	rr = makeRequest(t, srv, http.MethodPut, "/api/v1/users/"+user.ID+"/role", key, `{"role":"auditor"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("assign auditor: %d %s", rr.Code, rr.Body.String())
	}

	// Verify
	rr = makeRequest(t, srv, http.MethodGet, "/api/v1/users/"+user.ID+"/role", key, "")
	var roleResp struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &roleResp)
	if roleResp.Role != "auditor" {
		t.Fatalf("expected auditor, got %s", roleResp.Role)
	}
}

// TestCustomRolePermissionsResolveViaAPI verifies custom role permissions are resolved at login.
func TestCustomRolePermissionsResolveViaAPI(t *testing.T) {
	store := newCustomRoleStore(t)
	perms := []auth.Permission{auth.PermFleetRead, auth.PermApprovalRead}
	if _, err := store.Create("my-custom", perms, "Custom role"); err != nil {
		t.Fatal(err)
	}

	resolver := &roleResolver{customRoles: store}

	// Built-in role should still work
	adminPerms := resolver.PermissionsForRole("admin")
	found := false
	for _, p := range adminPerms {
		if p == auth.PermAdmin {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("admin permission resolver should return PermAdmin")
	}

	// Custom role should resolve
	customPerms := resolver.PermissionsForRole("my-custom")
	if len(customPerms) != 2 {
		t.Fatalf("expected 2 permissions for custom role, got %d: %v", len(customPerms), customPerms)
	}

	// Unknown role should return empty
	unknown := resolver.PermissionsForRole("no-such-role")
	if len(unknown) != 0 {
		t.Fatalf("expected empty permissions for unknown role, got %v", unknown)
	}
}

// makeAdminRequestWithBody is a helper for making admin requests.
func makeAdminRequestWithBody(t *testing.T, srv *Server, method, path, token string, bodyObj any) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if bodyObj != nil {
		var err error
		body, err = json.Marshal(bodyObj)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)
	return rr
}
