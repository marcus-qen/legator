package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/sandbox"
	"go.uber.org/zap"
)

// newSandboxTestServer creates a Server with sandbox enabled and auth disabled.
func newSandboxTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()

	t.Setenv("LEGATOR_AUTH", "0")

	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = t.TempDir()
	if mutate != nil {
		mutate(&cfg)
	}

	srv, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.Close)

	if srv.sandboxHandler == nil {
		t.Fatal("expected sandbox handler to be initialised")
	}
	return srv
}

// sandboxRequest is a convenience helper for sandbox API calls.
func sandboxRequest(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody *bytes.Reader
	if body == "" {
		reqBody = bytes.NewReader(nil)
	} else {
		reqBody = bytes.NewReader([]byte(body))
	}

	req := httptest.NewRequest(method, path, reqBody)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)
	return rr
}

// ── Route wiring tests ────────────────────────────────────────────────────────

func TestSandboxRoutes_CreateListGetDestroyTransition(t *testing.T) {
	srv := newSandboxTestServer(t, nil)

	// POST /api/v1/sandboxes — create
	createBody := `{"workspace_id":"ws-1","runtime_class":"kata","created_by":"alice","ttl_seconds":300}`
	cResp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", createBody)
	if cResp.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", cResp.Code, cResp.Body.String())
	}
	var created sandbox.SandboxSession
	if err := json.NewDecoder(cResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected session ID in create response")
	}
	if created.State != sandbox.StateCreated {
		t.Fatalf("expected state %q, got %q", sandbox.StateCreated, created.State)
	}

	// GET /api/v1/sandboxes — list
	lResp := sandboxRequest(t, srv, http.MethodGet, "/api/v1/sandboxes", "")
	if lResp.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d body=%s", lResp.Code, lResp.Body.String())
	}
	var listResult map[string]any
	_ = json.NewDecoder(lResp.Body).Decode(&listResult)
	if listResult["total"].(float64) < 1 {
		t.Fatal("expected at least 1 session in list")
	}

	// GET /api/v1/sandboxes/{id}
	gResp := sandboxRequest(t, srv, http.MethodGet, "/api/v1/sandboxes/"+created.ID, "")
	if gResp.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d body=%s", gResp.Code, gResp.Body.String())
	}
	var got sandbox.SandboxSession
	_ = json.NewDecoder(gResp.Body).Decode(&got)
	if got.ID != created.ID {
		t.Fatalf("get: wrong ID %q != %q", got.ID, created.ID)
	}

	// POST /api/v1/sandboxes/{id}/transition — created → provisioning
	tBody := `{"from":"created","to":"provisioning"}`
	tResp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tBody)
	if tResp.Code != http.StatusOK {
		t.Fatalf("transition: expected 200, got %d body=%s", tResp.Code, tResp.Body.String())
	}
	var transitioned sandbox.SandboxSession
	_ = json.NewDecoder(tResp.Body).Decode(&transitioned)
	if transitioned.State != sandbox.StateProvisioning {
		t.Fatalf("transition: expected provisioning, got %q", transitioned.State)
	}

	// Advance to ready then destroy.
	tBody2 := `{"from":"provisioning","to":"ready"}`
	sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tBody2)

	// DELETE /api/v1/sandboxes/{id}
	dResp := sandboxRequest(t, srv, http.MethodDelete, "/api/v1/sandboxes/"+created.ID, "")
	if dResp.Code != http.StatusOK {
		t.Fatalf("destroy: expected 200, got %d body=%s", dResp.Code, dResp.Body.String())
	}
	var destroyed sandbox.SandboxSession
	_ = json.NewDecoder(dResp.Body).Decode(&destroyed)
	if destroyed.State != sandbox.StateDestroyed {
		t.Fatalf("destroy: expected destroyed state, got %q", destroyed.State)
	}
}

func TestSandboxRoutes_GetNotFound(t *testing.T) {
	srv := newSandboxTestServer(t, nil)
	resp := sandboxRequest(t, srv, http.MethodGet, "/api/v1/sandboxes/no-such-id", "")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.Code)
	}
}

// ── Policy tests ──────────────────────────────────────────────────────────────

func TestSandboxPolicy_AllowedRuntime_Accepted(t *testing.T) {
	srv := newSandboxTestServer(t, func(cfg *config.Config) {
		cfg.Sandbox.AllowedRuntimes = []string{"kata", "gvisor"}
	})

	body := `{"workspace_id":"ws-1","runtime_class":"kata"}`
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201 for allowed runtime, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSandboxPolicy_AllowedRuntime_Rejected(t *testing.T) {
	srv := newSandboxTestServer(t, func(cfg *config.Config) {
		cfg.Sandbox.AllowedRuntimes = []string{"kata", "gvisor"}
	})

	body := `{"workspace_id":"ws-1","runtime_class":"docker"}`
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed runtime, got %d body=%s", resp.Code, resp.Body.String())
	}
	var errResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp["code"] != "runtime_not_allowed" {
		t.Fatalf("expected code runtime_not_allowed, got %v", errResp["code"])
	}
}

func TestSandboxPolicy_MaxConcurrent_Enforced(t *testing.T) {
	srv := newSandboxTestServer(t, func(cfg *config.Config) {
		cfg.Sandbox.MaxConcurrent = 2
	})

	// Create 2 sessions — both should succeed.
	for i := 0; i < 2; i++ {
		body := `{"workspace_id":"ws-1","runtime_class":"kata"}`
		resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
		if resp.Code != http.StatusCreated {
			t.Fatalf("session %d: expected 201, got %d body=%s", i+1, resp.Code, resp.Body.String())
		}
	}

	// Third session must be rejected.
	body := `{"workspace_id":"ws-1","runtime_class":"kata"}`
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when max concurrent reached, got %d body=%s", resp.Code, resp.Body.String())
	}
	var errResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp["code"] != "max_concurrent_exceeded" {
		t.Fatalf("expected code max_concurrent_exceeded, got %v", errResp["code"])
	}
}

func TestSandboxPolicy_MaxConcurrent_TerminalNotCounted(t *testing.T) {
	// Terminal sessions must not count towards the concurrent limit.
	srv := newSandboxTestServer(t, func(cfg *config.Config) {
		cfg.Sandbox.MaxConcurrent = 1
	})

	// Create a session and immediately destroy it.
	createBody := `{"workspace_id":"ws-1","runtime_class":"kata"}`
	cResp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", createBody)
	if cResp.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", cResp.Code, cResp.Body.String())
	}
	var s1 sandbox.SandboxSession
	_ = json.NewDecoder(cResp.Body).Decode(&s1)

	// Advance to ready, then destroy.
	for _, step := range []string{
		`{"from":"created","to":"provisioning"}`,
		`{"from":"provisioning","to":"ready"}`,
	} {
		sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes/"+s1.ID+"/transition", step)
	}
	sandboxRequest(t, srv, http.MethodDelete, "/api/v1/sandboxes/"+s1.ID, "")

	// Now create another session — should succeed because the first is destroyed.
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", createBody)
	if resp.Code != http.StatusCreated {
		t.Fatalf("second create after destroy: expected 201, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSandboxPolicy_ProbeValidation_UnknownProbe(t *testing.T) {
	srv := newSandboxTestServer(t, nil)
	// probe-ghost is not registered in the fleet.
	body := `{"workspace_id":"ws-1","runtime_class":"kata","probe_id":"probe-ghost"}`
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown probe, got %d body=%s", resp.Code, resp.Body.String())
	}
	var errResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp["code"] != "probe_not_found" {
		t.Fatalf("expected code probe_not_found, got %v", errResp["code"])
	}
}

func TestSandboxPolicy_ProbeValidation_KnownProbe(t *testing.T) {
	srv := newSandboxTestServer(t, nil)
	// Register a known probe.
	srv.fleetMgr.Register("probe-known", "probe-known", "linux", "amd64")

	body := `{"workspace_id":"ws-1","runtime_class":"kata","probe_id":"probe-known"}`
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201 for known probe, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSandboxPolicy_NoProbeID_Skips_Validation(t *testing.T) {
	srv := newSandboxTestServer(t, nil)
	// No probe_id supplied — validation must be skipped.
	body := `{"workspace_id":"ws-1","runtime_class":"kata"}`
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201 when probe_id absent, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSandboxRoutes_MissingRuntimeClass(t *testing.T) {
	srv := newSandboxTestServer(t, nil)
	body := `{"workspace_id":"ws-1"}`
	resp := sandboxRequest(t, srv, http.MethodPost, "/api/v1/sandboxes", body)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing runtime_class, got %d", resp.Code)
	}
}
