package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/api"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
)

func makeRequestWithSession(t *testing.T, srv *Server, method, path, sessionToken, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody *bytes.Reader
	if body == "" {
		reqBody = bytes.NewReader(nil)
	} else {
		reqBody = bytes.NewReader([]byte(body))
	}

	req := httptest.NewRequest(method, path, reqBody)
	if sessionToken != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)
	return rr
}

func mustSessionToken(t *testing.T, srv *Server, userID string) string {
	t.Helper()
	token, err := srv.sessionCreator.Create(userID)
	if err != nil {
		t.Fatalf("create session token: %v", err)
	}
	return token
}

func TestTenantAPI_CRUDAndAssignment(t *testing.T) {
	srv := newAuthTestServer(t)
	adminToken := createAPIKey(t, srv, "tenant-admin", auth.PermAdmin)

	// POST /api/v1/tenants
	createBody := `{"name":"Acme","slug":"acme","contact_email":"ops@acme.com"}`
	createResp := makeRequest(t, srv, http.MethodPost, "/api/v1/tenants", adminToken, createBody)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create tenant: expected 201, got %d body=%s", createResp.Code, createResp.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(createResp.Body.Bytes(), &created)
	tenantID, _ := created["id"].(string)
	if tenantID == "" {
		t.Fatalf("create tenant: missing id in response: %s", createResp.Body.String())
	}

	// GET /api/v1/tenants
	listResp := makeRequest(t, srv, http.MethodGet, "/api/v1/tenants", adminToken, "")
	if listResp.Code != http.StatusOK {
		t.Fatalf("list tenants: expected 200, got %d body=%s", listResp.Code, listResp.Body.String())
	}

	// GET /api/v1/tenants/{id}
	getResp := makeRequest(t, srv, http.MethodGet, "/api/v1/tenants/"+tenantID, adminToken, "")
	if getResp.Code != http.StatusOK {
		t.Fatalf("get tenant: expected 200, got %d body=%s", getResp.Code, getResp.Body.String())
	}

	// PATCH /api/v1/tenants/{id}
	patchBody := `{"name":"Acme Updated","contact_email":"noc@acme.com"}`
	patchResp := makeRequest(t, srv, http.MethodPatch, "/api/v1/tenants/"+tenantID, adminToken, patchBody)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("patch tenant: expected 200, got %d body=%s", patchResp.Code, patchResp.Body.String())
	}

	// PUT /api/v1/users/{id}/tenants
	u, err := srv.userStore.Create("tenantuser", "Tenant User", "secret", string(auth.RoleOperator))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	assignBody := `{"tenant_ids":["` + tenantID + `"]}`
	assignResp := makeRequest(t, srv, http.MethodPut, "/api/v1/users/"+u.ID+"/tenants", adminToken, assignBody)
	if assignResp.Code != http.StatusOK {
		t.Fatalf("assign tenants: expected 200, got %d body=%s", assignResp.Code, assignResp.Body.String())
	}
	ids, err := srv.tenantStore.GetUserTenants(u.ID)
	if err != nil {
		t.Fatalf("get user tenants: %v", err)
	}
	if len(ids) != 1 || ids[0] != tenantID {
		t.Fatalf("unexpected user tenants: %v", ids)
	}

	// DELETE /api/v1/tenants/{id}
	delResp := makeRequest(t, srv, http.MethodDelete, "/api/v1/tenants/"+tenantID, adminToken, "")
	if delResp.Code != http.StatusNoContent {
		t.Fatalf("delete tenant: expected 204, got %d body=%s", delResp.Code, delResp.Body.String())
	}
}

func TestTenantAPI_DeleteBlockedWhenProbesExist(t *testing.T) {
	srv := newAuthTestServer(t)
	adminToken := createAPIKey(t, srv, "tenant-admin", auth.PermAdmin)

	tnt, err := srv.tenantStore.Create("Beta", "beta", "")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	srv.fleetMgr.Register("probe-beta", "probe-beta", "linux", "amd64")
	if err := srv.fleetMgr.SetTenantID("probe-beta", tnt.ID); err != nil {
		t.Fatalf("set tenant id: %v", err)
	}

	resp := makeRequest(t, srv, http.MethodDelete, "/api/v1/tenants/"+tnt.ID, adminToken, "")
	if resp.Code != http.StatusConflict {
		t.Fatalf("expected 409 when deleting tenant with probes, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestTenantProbeIsolation_UserScopedAndAdminAll(t *testing.T) {
	srv := newAuthTestServer(t)

	tenantA, err := srv.tenantStore.Create("Tenant A", "tenant-a", "")
	if err != nil {
		t.Fatalf("create tenant A: %v", err)
	}
	tenantB, err := srv.tenantStore.Create("Tenant B", "tenant-b", "")
	if err != nil {
		t.Fatalf("create tenant B: %v", err)
	}

	userA, err := srv.userStore.Create("alice", "Alice", "pw-a", string(auth.RoleOperator))
	if err != nil {
		t.Fatalf("create user A: %v", err)
	}
	userB, err := srv.userStore.Create("bob", "Bob", "pw-b", string(auth.RoleOperator))
	if err != nil {
		t.Fatalf("create user B: %v", err)
	}
	admin, err := srv.userStore.Create("admin2", "Admin", "pw-admin", string(auth.RoleAdmin))
	if err != nil {
		t.Fatalf("create admin user: %v", err)
	}

	if err := srv.tenantStore.SetUserTenants(userA.ID, []string{tenantA.ID}); err != nil {
		t.Fatalf("set tenants user A: %v", err)
	}
	if err := srv.tenantStore.SetUserTenants(userB.ID, []string{tenantB.ID}); err != nil {
		t.Fatalf("set tenants user B: %v", err)
	}

	srv.fleetMgr.Register("probe-a", "probe-a", "linux", "amd64")
	srv.fleetMgr.Register("probe-b", "probe-b", "linux", "amd64")
	if err := srv.fleetMgr.SetTenantID("probe-a", tenantA.ID); err != nil {
		t.Fatalf("set tenant id probe-a: %v", err)
	}
	if err := srv.fleetMgr.SetTenantID("probe-b", tenantB.ID); err != nil {
		t.Fatalf("set tenant id probe-b: %v", err)
	}

	tokA := mustSessionToken(t, srv, userA.ID)
	tokB := mustSessionToken(t, srv, userB.ID)
	tokAdmin := mustSessionToken(t, srv, admin.ID)

	respA := makeRequestWithSession(t, srv, http.MethodGet, "/api/v1/probes", tokA, "")
	if respA.Code != http.StatusOK {
		t.Fatalf("user A list probes: expected 200, got %d body=%s", respA.Code, respA.Body.String())
	}
	var probesA []fleet.ProbeState
	_ = json.Unmarshal(respA.Body.Bytes(), &probesA)
	if len(probesA) != 1 || probesA[0].ID != "probe-a" {
		t.Fatalf("user A should see only probe-a, got %+v", probesA)
	}

	respB := makeRequestWithSession(t, srv, http.MethodGet, "/api/v1/probes", tokB, "")
	if respB.Code != http.StatusOK {
		t.Fatalf("user B list probes: expected 200, got %d body=%s", respB.Code, respB.Body.String())
	}
	var probesB []fleet.ProbeState
	_ = json.Unmarshal(respB.Body.Bytes(), &probesB)
	if len(probesB) != 1 || probesB[0].ID != "probe-b" {
		t.Fatalf("user B should see only probe-b, got %+v", probesB)
	}

	respAdmin := makeRequestWithSession(t, srv, http.MethodGet, "/api/v1/probes", tokAdmin, "")
	if respAdmin.Code != http.StatusOK {
		t.Fatalf("admin list probes: expected 200, got %d body=%s", respAdmin.Code, respAdmin.Body.String())
	}
	var probesAdmin []fleet.ProbeState
	_ = json.Unmarshal(respAdmin.Body.Bytes(), &probesAdmin)
	ids := make([]string, 0, len(probesAdmin))
	for _, ps := range probesAdmin {
		ids = append(ids, ps.ID)
	}
	sort.Strings(ids)
	if !contains(ids, "probe-a") || !contains(ids, "probe-b") {
		t.Fatalf("admin should see both tenant probes, got ids=%v", ids)
	}
}

func TestRegisterTokenTenantInheritance(t *testing.T) {
	srv := newAuthTestServer(t)

	tnt, err := srv.tenantStore.Create("Gamma", "gamma", "")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	tok := srv.tokenStore.GenerateWithOptions(api.GenerateOptions{TenantID: tnt.ID})

	body := `{"token":"` + tok.Value + `","hostname":"tenant-host","os":"linux","arch":"amd64"}`
	resp := makeRequest(t, srv, http.MethodPost, "/api/v1/register", "", body)
	if resp.Code != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d body=%s", resp.Code, resp.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(resp.Body.Bytes(), &out)
	probeID, _ := out["probe_id"].(string)
	if probeID == "" {
		t.Fatalf("register response missing probe_id: %s", resp.Body.String())
	}
	ps, ok := srv.fleetMgr.Get(probeID)
	if !ok {
		t.Fatalf("probe not found after registration: %s", probeID)
	}
	if ps.TenantID != tnt.ID {
		t.Fatalf("expected probe tenant %q, got %q", tnt.ID, ps.TenantID)
	}
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
