package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

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
