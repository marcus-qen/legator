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
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
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

	reliability := makeRequest(t, srv, http.MethodGet, "/api/v1/reliability/scorecard", token, "")
	if reliability.Code != http.StatusForbidden {
		t.Fatalf("expected reliability scorecard to be forbidden for command-only key, got %d body=%s", reliability.Code, reliability.Body.String())
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

	fedInv := makeRequest(t, srv, http.MethodGet, "/api/v1/federation/inventory", token, "")
	if fedInv.Code == http.StatusUnauthorized || fedInv.Code == http.StatusForbidden {
		t.Fatalf("expected federation inventory access, got %d body=%s", fedInv.Code, fedInv.Body.String())
	}

	fedSummary := makeRequest(t, srv, http.MethodGet, "/api/v1/federation/summary", token, "")
	if fedSummary.Code == http.StatusUnauthorized || fedSummary.Code == http.StatusForbidden {
		t.Fatalf("expected federation summary access, got %d body=%s", fedSummary.Code, fedSummary.Body.String())
	}

	history := makeRequest(t, srv, http.MethodGet, "/api/v1/fleet/chat", token, "")
	if history.Code == http.StatusUnauthorized || history.Code == http.StatusForbidden {
		t.Fatalf("expected fleet chat read access, got %d body=%s", history.Code, history.Body.String())
	}

	reliability := makeRequest(t, srv, http.MethodGet, "/api/v1/reliability/scorecard", token, "")
	if reliability.Code == http.StatusUnauthorized || reliability.Code == http.StatusForbidden {
		t.Fatalf("expected reliability scorecard read access, got %d body=%s", reliability.Code, reliability.Body.String())
	}
}

func TestFederationScopeAuthorizationAndSegmentation(t *testing.T) {
	srv := newAuthTestServer(t)

	tenantA := &fakeFederationSourceAdapter{
		source: fleet.FederationSourceDescriptor{ID: "tenant-a-source", Name: "Tenant A", Kind: "cluster", Cluster: "primary", Site: "dc-1", TenantID: "tenant-a", OrgID: "org-a", ScopeID: "scope-a"},
		result: fleet.FederationSourceResult{Inventory: fleet.FleetInventory{Probes: []fleet.ProbeInventorySummary{{ID: "probe-a", Hostname: "a-01", Status: "online", OS: "linux"}}}},
	}
	tenantB := &fakeFederationSourceAdapter{
		source: fleet.FederationSourceDescriptor{ID: "tenant-b-source", Name: "Tenant B", Kind: "cluster", Cluster: "primary", Site: "dc-2", TenantID: "tenant-b", OrgID: "org-b", ScopeID: "scope-b"},
		result: fleet.FederationSourceResult{Inventory: fleet.FleetInventory{Probes: []fleet.ProbeInventorySummary{{ID: "probe-b", Hostname: "b-01", Status: "online", OS: "linux"}}}},
	}
	srv.federationStore.RegisterSource(tenantA)
	srv.federationStore.RegisterSource(tenantB)

	token := createAPIKey(t, srv, "tenant-a-read",
		auth.PermFleetRead,
		auth.Permission("tenant:tenant-a"),
		auth.Permission("org:org-a"),
		auth.Permission("scope:scope-a"),
	)

	segmented := makeRequest(t, srv, http.MethodGet, "/api/v1/federation/inventory", token, "")
	if segmented.Code != http.StatusOK {
		t.Fatalf("expected tenant scoped federation read to succeed, got %d body=%s", segmented.Code, segmented.Body.String())
	}
	var payload fleet.FederatedInventory
	if err := json.Unmarshal(segmented.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode inventory payload: %v", err)
	}
	if len(payload.Probes) != 1 || payload.Probes[0].Probe.ID != "probe-a" {
		t.Fatalf("expected tenant-a data only, got %+v", payload.Probes)
	}
	if payload.Probes[0].Source.TenantID != "tenant-a" || payload.Probes[0].Source.ScopeID != "scope-a" {
		t.Fatalf("expected tenant/scope attribution in payload, got %+v", payload.Probes[0].Source)
	}

	forbidden := makeRequest(t, srv, http.MethodGet, "/api/v1/federation/inventory?tenant_id=tenant-b&scope_id=scope-b", token, "")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden tenant access, got %d body=%s", forbidden.Code, forbidden.Body.String())
	}
	var errPayload APIError
	if err := json.Unmarshal(forbidden.Body.Bytes(), &errPayload); err != nil {
		t.Fatalf("decode forbidden payload: %v", err)
	}
	if errPayload.Code != "forbidden_scope" {
		t.Fatalf("expected forbidden_scope code, got %+v", errPayload)
	}

	authzEvents := srv.queryAudit(audit.Filter{Type: audit.EventAuthorizationDenied, Limit: 10})
	if len(authzEvents) == 0 {
		t.Fatal("expected authorization denied audit event for forbidden federation scope")
	}
	foundDeniedContext := false
	for _, evt := range authzEvents {
		detail, ok := evt.Detail.(map[string]any)
		if !ok {
			continue
		}
		if detail["path"] != "/api/v1/federation/inventory" {
			continue
		}
		if detail["requested_tenant_id"] != "tenant-b" || detail["requested_scope_id"] != "scope-b" {
			continue
		}
		allowedOK := false
		switch scopes := detail["allowed_scope_ids"].(type) {
		case []string:
			allowedOK = len(scopes) > 0 && scopes[0] == "scope-a"
		case []any:
			allowedOK = len(scopes) > 0 && scopes[0] == "scope-a"
		}
		if !allowedOK {
			continue
		}
		foundDeniedContext = true
		break
	}
	if !foundDeniedContext {
		t.Fatalf("expected denied audit detail to include tenant/scope context, events=%+v", authzEvents)
	}

	readEvents := srv.queryAudit(audit.Filter{Type: audit.EventFederationRead, Limit: 10})
	if len(readEvents) == 0 {
		t.Fatal("expected federation read audit event")
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
		{name: "automation pack create", method: http.MethodPost, path: "/api/v1/automation-packs", body: `{"metadata":{"id":"blocked.pack","name":"blocked","version":"1.0.0"},"steps":[{"id":"s1","action":"noop","expected_outcomes":[{"description":"ok","success_criteria":"ok"}]}],"expected_outcomes":[{"description":"done","success_criteria":"done"}]}`},
		{name: "automation pack dry-run", method: http.MethodPost, path: "/api/v1/automation-packs/dry-run", body: `{"definition":{"metadata":{"id":"blocked.pack","name":"blocked","version":"1.0.0"},"steps":[{"id":"s1","action":"ls","expected_outcomes":[{"description":"ok","success_criteria":"ok"}]}],"expected_outcomes":[{"description":"done","success_criteria":"done"}]}}`},
		{name: "automation pack execute", method: http.MethodPost, path: "/api/v1/automation-packs/blocked.pack/executions", body: `{}`},
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

func TestPermissionsAutomationPackRoutes(t *testing.T) {
	srv := newAuthTestServer(t)
	readToken := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	createBody := `{"metadata":{"id":"ops.backup","name":"Ops Backup","version":"1.0.0"},"steps":[{"id":"prepare","action":"run_command","expected_outcomes":[{"description":"prepare done","success_criteria":"exit_code == 0"}]}],"expected_outcomes":[{"description":"workflow complete","success_criteria":"all required outcomes met"}]}`

	created := makeRequest(t, srv, http.MethodPost, "/api/v1/automation-packs", writeToken, createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201 create with fleet:write, got %d body=%s", created.Code, created.Body.String())
	}

	listRead := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs", readToken, "")
	if listRead.Code == http.StatusUnauthorized || listRead.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to list automation packs, got %d body=%s", listRead.Code, listRead.Body.String())
	}

	var listPayload struct {
		AutomationPacks []struct {
			Metadata struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			} `json:"metadata"`
		} `json:"automation_packs"`
	}
	if err := json.Unmarshal(listRead.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode automation pack list payload: %v", err)
	}
	if len(listPayload.AutomationPacks) == 0 {
		t.Fatalf("expected at least one automation pack in list: %s", listRead.Body.String())
	}
	packID := listPayload.AutomationPacks[0].Metadata.ID

	getRead := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/"+packID, readToken, "")
	if getRead.Code == http.StatusUnauthorized || getRead.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to get automation pack, got %d body=%s", getRead.Code, getRead.Body.String())
	}

	listWrite := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs", writeToken, "")
	if listWrite.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:write-only token to be denied on read route, got %d body=%s", listWrite.Code, listWrite.Body.String())
	}

	dryRunBody := `{"definition":{"metadata":{"id":"ops.backup","name":"Ops Backup","version":"1.0.0"},"inputs":[{"name":"environment","type":"string","required":true}],"steps":[{"id":"prepare","action":"systemctl restart nginx","expected_outcomes":[{"description":"prepare done","success_criteria":"exit_code == 0"}]}],"expected_outcomes":[{"description":"workflow complete","success_criteria":"all required outcomes met"}]},"inputs":{"environment":"prod"}}`
	dryRunWrite := makeRequest(t, srv, http.MethodPost, "/api/v1/automation-packs/dry-run", writeToken, dryRunBody)
	if dryRunWrite.Code == http.StatusUnauthorized || dryRunWrite.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:write to access dry-run route, got %d body=%s", dryRunWrite.Code, dryRunWrite.Body.String())
	}

	runWrite := makeRequest(t, srv, http.MethodPost, "/api/v1/automation-packs/"+packID+"/executions", writeToken, `{"inputs":{"environment":"prod"}}`)
	if runWrite.Code == http.StatusUnauthorized || runWrite.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:write to access execution route, got %d body=%s", runWrite.Code, runWrite.Body.String())
	}

	var runPayload struct {
		Execution struct {
			ID string `json:"id"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(runWrite.Body.Bytes(), &runPayload); err != nil {
		t.Fatalf("decode execution payload: %v body=%s", err, runWrite.Body.String())
	}
	if runPayload.Execution.ID != "" {
		getExecutionRead := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/executions/"+runPayload.Execution.ID, readToken, "")
		if getExecutionRead.Code == http.StatusUnauthorized || getExecutionRead.Code == http.StatusForbidden {
			t.Fatalf("expected fleet:read to get execution status, got %d body=%s", getExecutionRead.Code, getExecutionRead.Body.String())
		}

		getTimelineRead := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/executions/"+runPayload.Execution.ID+"/timeline", readToken, "")
		if getTimelineRead.Code == http.StatusUnauthorized || getTimelineRead.Code == http.StatusForbidden {
			t.Fatalf("expected fleet:read to get execution timeline, got %d body=%s", getTimelineRead.Code, getTimelineRead.Body.String())
		}

		getArtifactsRead := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/executions/"+runPayload.Execution.ID+"/artifacts", readToken, "")
		if getArtifactsRead.Code == http.StatusUnauthorized || getArtifactsRead.Code == http.StatusForbidden {
			t.Fatalf("expected fleet:read to get execution artifacts, got %d body=%s", getArtifactsRead.Code, getArtifactsRead.Body.String())
		}
	}

	createDenied := makeRequest(t, srv, http.MethodPost, "/api/v1/automation-packs", readToken, createBody)
	if createDenied.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:read token to be denied for create, got %d body=%s", createDenied.Code, createDenied.Body.String())
	}

	dryRunDenied := makeRequest(t, srv, http.MethodPost, "/api/v1/automation-packs/dry-run", readToken, dryRunBody)
	if dryRunDenied.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:read token to be denied for dry-run, got %d body=%s", dryRunDenied.Code, dryRunDenied.Body.String())
	}

	runDenied := makeRequest(t, srv, http.MethodPost, "/api/v1/automation-packs/"+packID+"/executions", readToken, `{}`)
	if runDenied.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:read token to be denied for execution start, got %d body=%s", runDenied.Code, runDenied.Body.String())
	}

	getExecutionRead := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/executions/does-not-exist", readToken, "")
	if getExecutionRead.Code == http.StatusUnauthorized || getExecutionRead.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to access execution read route, got %d body=%s", getExecutionRead.Code, getExecutionRead.Body.String())
	}

	getExecutionWrite := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/executions/does-not-exist", writeToken, "")
	if getExecutionWrite.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:write-only token to be denied on execution read route, got %d body=%s", getExecutionWrite.Code, getExecutionWrite.Body.String())
	}

	getTimelineWrite := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/executions/does-not-exist/timeline", writeToken, "")
	if getTimelineWrite.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:write-only token to be denied on timeline read route, got %d body=%s", getTimelineWrite.Code, getTimelineWrite.Body.String())
	}

	getArtifactsWrite := makeRequest(t, srv, http.MethodGet, "/api/v1/automation-packs/executions/does-not-exist/artifacts", writeToken, "")
	if getArtifactsWrite.Code != http.StatusForbidden {
		t.Fatalf("expected fleet:write-only token to be denied on artifacts read route, got %d body=%s", getArtifactsWrite.Code, getArtifactsWrite.Body.String())
	}
}

func TestPermissionsKubeflowRoutes(t *testing.T) {
	srv := newAuthTestServer(t)
	readToken := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	for _, path := range []string{"/api/v1/kubeflow/status", "/api/v1/kubeflow/inventory", "/api/v1/kubeflow/runs/run-a/status"} {
		rr := makeRequest(t, srv, http.MethodGet, path, readToken, "")
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Fatalf("expected fleet:read to access %s, got %d body=%s", path, rr.Code, rr.Body.String())
		}
	}

	for _, path := range []string{"/api/v1/kubeflow/actions/refresh", "/api/v1/kubeflow/runs/submit", "/api/v1/kubeflow/runs/run-a/cancel"} {
		denied := makeRequest(t, srv, http.MethodPost, path, readToken, "{}")
		if denied.Code != http.StatusForbidden {
			t.Fatalf("expected fleet:read to be denied for %s, got %d body=%s", path, denied.Code, denied.Body.String())
		}

		allowed := makeRequest(t, srv, http.MethodPost, path, writeToken, "{}")
		if allowed.Code == http.StatusUnauthorized || allowed.Code == http.StatusForbidden {
			t.Fatalf("expected fleet:write to pass authz for %s, got %d body=%s", path, allowed.Code, allowed.Body.String())
		}
	}
}

func TestPermissionsGrafanaRoutes(t *testing.T) {
	srv := newAuthTestServer(t)
	readToken := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	for _, path := range []string{"/api/v1/grafana/status", "/api/v1/grafana/snapshot"} {
		readResp := makeRequest(t, srv, http.MethodGet, path, readToken, "")
		if readResp.Code == http.StatusUnauthorized || readResp.Code == http.StatusForbidden {
			t.Fatalf("expected fleet:read to access %s, got %d body=%s", path, readResp.Code, readResp.Body.String())
		}

		writeResp := makeRequest(t, srv, http.MethodGet, path, writeToken, "")
		if writeResp.Code != http.StatusForbidden {
			t.Fatalf("expected fleet:write-only token to be denied on read route %s, got %d body=%s", path, writeResp.Code, writeResp.Body.String())
		}
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
	if rr := makeRequest(t, srv, http.MethodGet, "/api/v1/jobs/runs", readToken, ""); rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to list all job runs, got %d body=%s", rr.Code, rr.Body.String())
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
		{method: http.MethodPost, path: "/api/v1/jobs/" + createdJob.ID + "/cancel"},
		{method: http.MethodPost, path: "/api/v1/jobs/" + createdJob.ID + "/runs/nonexistent/cancel"},
		{method: http.MethodPost, path: "/api/v1/jobs/" + createdJob.ID + "/runs/nonexistent/retry"},
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

func TestPermissionsJobsPageRequiresFleetRead(t *testing.T) {
	srv := newAuthTestServer(t)

	fleetRead := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)
	approvalRead := createAPIKey(t, srv, "approval-read", auth.PermApprovalRead)

	allowed := makeRequest(t, srv, http.MethodGet, "/jobs", fleetRead, "")
	if allowed.Code == http.StatusUnauthorized || allowed.Code == http.StatusForbidden {
		t.Fatalf("expected fleet:read to access /jobs, got %d body=%s", allowed.Code, allowed.Body.String())
	}

	denied := makeRequest(t, srv, http.MethodGet, "/jobs", approvalRead, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("expected /jobs to deny approval:read-only token, got %d body=%s", denied.Code, denied.Body.String())
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
