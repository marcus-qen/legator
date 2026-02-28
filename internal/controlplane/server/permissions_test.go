package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	"go.uber.org/zap"
)

func newAuthTestServer(t *testing.T) *Server {
	t.Helper()

	t.Setenv("LEGATOR_AUTH", "true")

	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = t.TempDir()
	cfg.AuthEnabled = true

	srv, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.Close)

	if srv.authStore == nil {
		t.Fatal("expected auth store to be initialized")
	}

	srv.fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")

	return srv
}

func createAPIKey(t *testing.T, srv *Server, name string, permissions ...auth.Permission) string {
	t.Helper()

	_, plain, err := srv.authStore.Create(name, permissions, nil)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	return plain
}

func makeRequest(t *testing.T, srv *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody *bytes.Reader
	if body == "" {
		reqBody = bytes.NewReader(nil)
	} else {
		reqBody = bytes.NewReader([]byte(body))
	}

	req := httptest.NewRequest(method, path, reqBody)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)
	return rr
}

func TestPermissionsRequireAuthentication(t *testing.T) {
	srv := newAuthTestServer(t)

	rr := makeRequest(t, srv, http.MethodGet, "/api/v1/probes", "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPermissionsFleetReadCannotDispatchCommand(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)

	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/probes/probe-1/command", token, `{"command":"id"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPermissionsAdminCanAccessAllScopes(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "admin", auth.PermAdmin)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "fleet read", method: http.MethodGet, path: "/api/v1/probes"},
		{name: "command exec", method: http.MethodPost, path: "/api/v1/probes/probe-1/command", body: `{"command":"id"}`},
		{name: "approval read", method: http.MethodGet, path: "/api/v1/approvals"},
		{name: "approval write", method: http.MethodPost, path: "/api/v1/approvals/missing/decide", body: `{"decision":"approved","decided_by":"admin"}`},
		{name: "audit read", method: http.MethodGet, path: "/api/v1/audit"},
		{name: "webhook manage", method: http.MethodGet, path: "/api/v1/webhooks"},
		{name: "webhook deliveries", method: http.MethodGet, path: "/api/v1/webhooks/deliveries"},
		{name: "fleet write", method: http.MethodDelete, path: "/api/v1/probes/missing"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := makeRequest(t, srv, tc.method, tc.path, token, tc.body)
			if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Fatalf("admin key unexpectedly denied: status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestPermissionsCommandExecCannotDeleteProbe(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "command", auth.PermCommandExec)

	dispatch := makeRequest(t, srv, http.MethodPost, "/api/v1/probes/probe-1/command", token, `{"command":"id"}`)
	if dispatch.Code == http.StatusUnauthorized || dispatch.Code == http.StatusForbidden {
		t.Fatalf("expected command dispatch to be allowed, got %d body=%s", dispatch.Code, dispatch.Body.String())
	}

	deleteProbe := makeRequest(t, srv, http.MethodDelete, "/api/v1/probes/probe-1", token, "")
	if deleteProbe.Code != http.StatusForbidden {
		t.Fatalf("expected delete probe to be forbidden, got %d body=%s", deleteProbe.Code, deleteProbe.Body.String())
	}
}

func TestPermissionsWebhookManageCanReadDeliveries(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "webhook-manage", auth.PermWebhookManage)

	rr := makeRequest(t, srv, http.MethodGet, "/api/v1/webhooks/deliveries", token, "")
	if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
		t.Fatalf("expected webhook manage to access deliveries, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPermissionsFleetReadCannotReadWebhookDeliveries(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)

	rr := makeRequest(t, srv, http.MethodGet, "/api/v1/webhooks/deliveries", token, "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPermissionsFleetReadCanAccessFleetInventoryAndChat(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)

	inv := makeRequest(t, srv, http.MethodGet, "/api/v1/fleet/inventory", token, "")
	if inv.Code == http.StatusUnauthorized || inv.Code == http.StatusForbidden {
		t.Fatalf("expected fleet inventory access, got %d body=%s", inv.Code, inv.Body.String())
	}

	history := makeRequest(t, srv, http.MethodGet, "/api/v1/fleet/chat", token, "")
	if history.Code == http.StatusUnauthorized || history.Code == http.StatusForbidden {
		t.Fatalf("expected fleet chat read access, got %d body=%s", history.Code, history.Body.String())
	}
}

func TestPermissionsFleetReadCannotMutateModelDockCloudOrDiscovery(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "model dock create", method: http.MethodPost, path: "/api/v1/model-profiles", body: `{"name":"blocked","provider":"openai","base_url":"https://api.example.com/v1","model":"gpt-4o-mini","api_key":"secret"}`},
		{name: "cloud connector create", method: http.MethodPost, path: "/api/v1/cloud/connectors", body: `{"name":"blocked","provider":"aws","auth_mode":"cli","is_enabled":true}`},
		{name: "discovery scan", method: http.MethodPost, path: "/api/v1/discovery/scan", body: `{"cidr":"127.0.0.0/24"}`},
		{name: "discovery install token", method: http.MethodPost, path: "/api/v1/discovery/install-token"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := makeRequest(t, srv, tc.method, tc.path, token, tc.body)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestPermissionsNetworkDeviceRoutes(t *testing.T) {
	srv := newAuthTestServer(t)
	readToken := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	createBody := `{"name":"core-rtr","host":"10.0.0.10","port":22,"vendor":"cisco","username":"admin","auth_mode":"password","tags":["core"]}`
	created := makeRequest(t, srv, http.MethodPost, "/api/v1/network/devices", writeToken, createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201 create with fleet:write, got %d body=%s", created.Code, created.Body.String())
	}

	listRead := makeRequest(t, srv, http.MethodGet, "/api/v1/network/devices", readToken, "")
	if listRead.Code == http.StatusUnauthorized || listRead.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to list network devices, got %d body=%s", listRead.Code, listRead.Body.String())
	}

	var listPayload struct {
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(listRead.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list payload: %v", err)
	}
	if len(listPayload.Devices) == 0 || listPayload.Devices[0].ID == "" {
		t.Fatalf("expected at least one network device in list, body=%s", listRead.Body.String())
	}
	deviceID := listPayload.Devices[0].ID

	getRead := makeRequest(t, srv, http.MethodGet, "/api/v1/network/devices/"+deviceID, readToken, "")
	if getRead.Code == http.StatusUnauthorized || getRead.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to get network device, got %d body=%s", getRead.Code, getRead.Body.String())
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create denied", method: http.MethodPost, path: "/api/v1/network/devices", body: createBody},
		{name: "update denied", method: http.MethodPut, path: "/api/v1/network/devices/" + deviceID, body: `{"name":"new"}`},
		{name: "delete denied", method: http.MethodDelete, path: "/api/v1/network/devices/" + deviceID},
		{name: "test denied", method: http.MethodPost, path: "/api/v1/network/devices/" + deviceID + "/test", body: `{}`},
		{name: "inventory denied", method: http.MethodPost, path: "/api/v1/network/devices/" + deviceID + "/inventory", body: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := makeRequest(t, srv, tc.method, tc.path, readToken, tc.body)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for fleet:read on %s %s, got %d body=%s", tc.method, tc.path, rr.Code, rr.Body.String())
			}
		})
	}

	updateWrite := makeRequest(t, srv, http.MethodPut, "/api/v1/network/devices/"+deviceID, writeToken, `{"name":"core-rtr-2","tags":["core","edge"]}`)
	if updateWrite.Code == http.StatusUnauthorized || updateWrite.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:write update access, got %d body=%s", updateWrite.Code, updateWrite.Body.String())
	}

	testWrite := makeRequest(t, srv, http.MethodPost, "/api/v1/network/devices/"+deviceID+"/test", writeToken, `{}`)
	if testWrite.Code == http.StatusUnauthorized || testWrite.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:write test access, got %d body=%s", testWrite.Code, testWrite.Body.String())
	}

	inventoryWrite := makeRequest(t, srv, http.MethodPost, "/api/v1/network/devices/"+deviceID+"/inventory", writeToken, `{}`)
	if inventoryWrite.Code == http.StatusUnauthorized || inventoryWrite.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:write inventory access, got %d body=%s", inventoryWrite.Code, inventoryWrite.Body.String())
	}

	deleteWrite := makeRequest(t, srv, http.MethodDelete, "/api/v1/network/devices/"+deviceID, writeToken, "")
	if deleteWrite.Code == http.StatusUnauthorized || deleteWrite.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:write delete access, got %d body=%s", deleteWrite.Code, deleteWrite.Body.String())
	}
}

func TestPermissionsKubeflowRoutes(t *testing.T) {
	srv := newAuthTestServer(t)
	readToken := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	for _, path := range []string{"/api/v1/kubeflow/status", "/api/v1/kubeflow/inventory"} {
		rr := makeRequest(t, srv, http.MethodGet, path, readToken, "")
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Fatalf("expected fleet:read to access %s, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}

	refreshDenied := makeRequest(t, srv, http.MethodPost, "/api/v1/kubeflow/actions/refresh", readToken, "")
	if refreshDenied.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:read to be denied for refresh action, got %d body=%s", refreshDenied.Code, refreshDenied.Body.String())
	}

	refreshAllowed := makeRequest(t, srv, http.MethodPost, "/api/v1/kubeflow/actions/refresh", writeToken, "")
	if refreshAllowed.Code == http.StatusUnauthorized || refreshAllowed.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:write to pass authz for refresh route, got %d body=%s", refreshAllowed.Code, refreshAllowed.Body.String())
	}
}

func TestPermissionsJobsRoutes(t *testing.T) {
	srv := newAuthTestServer(t)
	readToken := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	createBody := `{"name":"nightly","command":"echo hi","schedule":"5m","target":{"kind":"probe","value":"probe-1"},"enabled":true}`
	created := makeRequest(t, srv, http.MethodPost, "/api/v1/jobs", writeToken, createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201 create with fleet:write, got %d body=%s", created.Code, created.Body.String())
	}

	var createdJob struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdJob); err != nil {
		t.Fatalf("decode created job: %v", err)
	}
	if createdJob.ID == "" {
		t.Fatalf("expected job id in create response: %s", created.Body.String())
	}

	if rr := makeRequest(t, srv, http.MethodGet, "/api/v1/jobs", readToken, ""); rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to list jobs, got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := makeRequest(t, srv, http.MethodGet, "/api/v1/jobs/"+createdJob.ID, readToken, ""); rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to get job, got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := makeRequest(t, srv, http.MethodGet, "/api/v1/jobs/"+createdJob.ID+"/runs", readToken, ""); rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to list job runs, got %d body=%s", rr.Code, rr.Body.String())
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/api/v1/jobs", body: createBody},
		{method: http.MethodPost, path: "/api/v1/jobs/" + createdJob.ID + "/run"},
		{method: http.MethodPost, path: "/api/v1/jobs/" + createdJob.ID + "/enable"},
		{method: http.MethodPost, path: "/api/v1/jobs/" + createdJob.ID + "/disable"},
		{method: http.MethodPut, path: "/api/v1/jobs/" + createdJob.ID, body: createBody},
		{method: http.MethodDelete, path: "/api/v1/jobs/" + createdJob.ID},
	} {
		rr := makeRequest(t, srv, tc.method, tc.path, readToken, tc.body)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected fleet:read to be denied for %s %s, got %d body=%s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
}

func TestPermissionsApprovalsAndAuditPagesUseScopeSpecificPermissions(t *testing.T) {
	srv := newAuthTestServer(t)

	fleetRead := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	approvalRead := createAPIKey(t, srv, "approval-read", auth.PermApprovalRead)
	auditRead := createAPIKey(t, srv, "audit-read", auth.PermAuditRead)

	approvalsDenied := makeRequest(t, srv, http.MethodGet, "/approvals", fleetRead, "")
	if approvalsDenied.Code != http.StatusForbidden {
		t.Fatalf("expected /approvals to require approval:read, got %d body=%s", approvalsDenied.Code, approvalsDenied.Body.String())
	}

	approvalsAllowed := makeRequest(t, srv, http.MethodGet, "/approvals", approvalRead, "")
	if approvalsAllowed.Code == http.StatusUnauthorized || approvalsAllowed.Code == http.StatusForbidden {
		t.Fatalf("expected approval:read to access /approvals, got %d body=%s", approvalsAllowed.Code, approvalsAllowed.Body.String())
	}

	auditDenied := makeRequest(t, srv, http.MethodGet, "/audit", fleetRead, "")
	if auditDenied.Code != http.StatusForbidden {
		t.Fatalf("expected /audit to require audit:read, got %d body=%s", auditDenied.Code, auditDenied.Body.String())
	}

	auditAllowed := makeRequest(t, srv, http.MethodGet, "/audit", auditRead, "")
	if auditAllowed.Code == http.StatusUnauthorized || auditAllowed.Code == http.StatusForbidden {
		t.Fatalf("expected audit:read to access /audit, got %d body=%s", auditAllowed.Code, auditAllowed.Body.String())
	}
}

func TestAuthorizationDenialsAreAudited(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)

	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/model-profiles", token, `{"name":"blocked"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}

	events := srv.queryAudit(audit.Filter{Type: audit.EventAuthorizationDenied, Limit: 5})
	if len(events) == 0 {
		t.Fatal("expected authorization denial audit event")
	}

	detail, ok := events[0].Detail.(map[string]string)
	if !ok {
		t.Fatalf("expected detail map[string]string, got %T", events[0].Detail)
	}
	if detail["path"] != "/api/v1/model-profiles" {
		t.Fatalf("expected denied path to be recorded, got %q", detail["path"])
	}
	if detail["required_permission"] != string(auth.PermFleetWrite) {
		t.Fatalf("expected required permission %q, got %q", auth.PermFleetWrite, detail["required_permission"])
	}
}
