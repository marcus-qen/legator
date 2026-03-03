package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"go.uber.org/zap"
)

// newIsolatedTestServer creates a test server with workspace isolation enabled.
func newIsolatedTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("LEGATOR_LLM_PROVIDER", "")
	t.Setenv("LEGATOR_AUTH", "0")
	t.Setenv("LEGATOR_SIGNING_KEY", strings.Repeat("a", 64))
	cfg := config.Config{
		ListenAddr:         ":0",
		DataDir:            t.TempDir(),
		WorkspaceIsolation: config.WorkspaceIsolationConfig{Enabled: true},
	}
	logger := zap.NewNop()
	srv, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("new isolated server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// doRequest executes an HTTP request against the test server's handler.
func doRequest(t *testing.T, srv *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)
	return rr
}

// withWorkspaceAPIKey injects a fake API key with a workspace claim.
func withWorkspaceAPIKey(r *http.Request, workspaceID string) *http.Request {
	key := &auth.APIKey{
		ID: "test-key-" + workspaceID,
		Permissions: []auth.Permission{
			auth.PermFleetRead, auth.PermFleetWrite,
			auth.PermApprovalRead, auth.PermApprovalWrite,
			auth.PermAuditRead,
			auth.Permission("workspace:" + workspaceID),
		},
	}
	ctx := auth.WithAPIKeyContext(r.Context(), key)
	return r.WithContext(ctx)
}

// withWildcardAPIKey injects a fake API key with workspace:* (admin-level).
func withWildcardAPIKey(r *http.Request) *http.Request {
	key := &auth.APIKey{
		ID: "admin-key",
		Permissions: []auth.Permission{
			auth.PermAdmin,
			auth.Permission("workspace:*"),
		},
	}
	ctx := auth.WithAPIKeyContext(r.Context(), key)
	return r.WithContext(ctx)
}

// TestWorkspaceIsolation_JobListScope verifies workspace-A token only sees workspace-A jobs.
func TestWorkspaceIsolation_JobListScope(t *testing.T) {
	srv := newIsolatedTestServer(t)
	if srv.jobsStore == nil {
		t.Skip("jobs store not available")
	}

	for _, tc := range []struct{ ws, name string }{{"ws-a", "job-alpha"}, {"ws-b", "job-beta"}} {
		_, err := srv.jobsStore.CreateJob(jobs.Job{
			WorkspaceID: tc.ws,
			Name:        tc.name,
			Command:     "echo " + tc.ws,
			Schedule:    "@hourly",
			Target:      jobs.Target{Kind: jobs.TargetKindAll},
			Enabled:     true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	req := withWorkspaceAPIKey(httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil), "ws-a")
	rr := doRequest(t, srv, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp []jobs.Job
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 job (ws-a only), got %d: %+v", len(resp), resp)
	}
	if resp[0].Name != "job-alpha" {
		t.Errorf("expected job-alpha, got %s", resp[0].Name)
	}
}

// TestWorkspaceIsolation_JobGetCrossWorkspaceForbidden verifies 403 on cross-workspace GET.
func TestWorkspaceIsolation_JobGetCrossWorkspaceForbidden(t *testing.T) {
	srv := newIsolatedTestServer(t)
	if srv.jobsStore == nil {
		t.Skip("jobs store not available")
	}

	jobA, err := srv.jobsStore.CreateJob(jobs.Job{
		WorkspaceID: "ws-a",
		Name:        "secret-job",
		Command:     "echo secret",
		Schedule:    "@hourly",
		Target:      jobs.Target{Kind: jobs.TargetKindAll},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// ws-b token cannot access ws-a job
	req := withWorkspaceAPIKey(httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobA.ID, nil), "ws-b")
	rr := doRequest(t, srv, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 cross-workspace GET, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestWorkspaceIsolation_WildcardTokenSeesAll verifies workspace:* bypasses isolation.
func TestWorkspaceIsolation_WildcardTokenSeesAll(t *testing.T) {
	srv := newIsolatedTestServer(t)
	if srv.jobsStore == nil {
		t.Skip("jobs store not available")
	}

	for _, ws := range []string{"ws-a", "ws-b"} {
		_, err := srv.jobsStore.CreateJob(jobs.Job{
			WorkspaceID: ws,
			Name:        "job-in-" + ws,
			Command:     "echo " + ws,
			Schedule:    "@hourly",
			Target:      jobs.Target{Kind: jobs.TargetKindAll},
			Enabled:     true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	req := withWildcardAPIKey(httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	rr := doRequest(t, srv, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp []jobs.Job
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 2 {
		t.Fatalf("wildcard token should see all 2 jobs, got %d", len(resp))
	}
}

// TestWorkspaceIsolation_DisabledByDefault verifies single-workspace mode is unaffected.
func TestWorkspaceIsolation_DisabledByDefault(t *testing.T) {
	srv := newTestServer(t) // isolation NOT enabled
	if srv.jobsStore == nil {
		t.Skip("jobs store not available")
	}
	if srv.cfg.WorkspaceIsolation.Enabled {
		t.Fatal("workspace isolation should be disabled by default")
	}

	for _, ws := range []string{"ws-a", "ws-b"} {
		_, err := srv.jobsStore.CreateJob(jobs.Job{
			WorkspaceID: ws,
			Name:        "job-in-" + ws,
			Command:     "echo " + ws,
			Schedule:    "@hourly",
			Target:      jobs.Target{Kind: jobs.TargetKindAll},
			Enabled:     true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Even with workspace-scoped token, see all jobs when isolation is off
	req := withWorkspaceAPIKey(httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil), "ws-a")
	rr := doRequest(t, srv, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp []jobs.Job
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 2 {
		t.Fatalf("isolation disabled: expected all 2 jobs, got %d", len(resp))
	}
}

// TestWorkspaceIsolation_ApprovalListScope verifies approval queue is workspace-scoped.
func TestWorkspaceIsolation_ApprovalListScope(t *testing.T) {
	srv := newIsolatedTestServer(t)

	_, err := srv.approvalQueue.SubmitWithWorkspace("ws-a", "probe-a", nil, "test reason", "high", "test", "queue", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.approvalQueue.SubmitWithWorkspace("ws-b", "probe-b", nil, "test reason", "high", "test", "queue", nil)
	if err != nil {
		t.Fatal(err)
	}

	req := withWorkspaceAPIKey(httptest.NewRequest(http.MethodGet, "/api/v1/approvals?status=pending", nil), "ws-a")
	rr := doRequest(t, srv, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	approvals, ok := result["approvals"].([]any)
	if !ok || len(approvals) != 1 {
		t.Fatalf("expected 1 pending approval for ws-a, got result=%+v", result)
	}
}

// TestWorkspaceIsolation_AuditLogScope verifies audit log is workspace-scoped.
func TestWorkspaceIsolation_AuditLogScope(t *testing.T) {
	srv := newIsolatedTestServer(t)

	srv.recordAudit(audit.Event{
		Type: audit.EventCommandSent, WorkspaceID: "ws-a",
		ProbeID: "probe-a", Actor: "alice", Summary: "cmd-a sent",
	})
	srv.recordAudit(audit.Event{
		Type: audit.EventCommandSent, WorkspaceID: "ws-b",
		ProbeID: "probe-b", Actor: "bob", Summary: "cmd-b sent",
	})

	req := withWorkspaceAPIKey(httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil), "ws-a")
	rr := doRequest(t, srv, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	events, ok := result["events"].([]any)
	if !ok {
		t.Fatalf("expected events in response: %+v", result)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event for ws-a, got %d", len(events))
	}
}

// TestWorkspaceIsolation_ApprovalGetCrossWorkspaceForbidden verifies 404 on
// cross-workspace approval GET (approvals are not visible across workspaces).
func TestWorkspaceIsolation_ApprovalGetCrossWorkspaceForbidden(t *testing.T) {
	srv := newIsolatedTestServer(t)

	req1, err := srv.approvalQueue.SubmitWithWorkspace("ws-a", "probe-a", nil, "reason", "high", "test", "queue", nil)
	if err != nil {
		t.Fatal(err)
	}

	// ws-b tries to access ws-a's approval by ID
	req := withWorkspaceAPIKey(httptest.NewRequest(http.MethodGet, "/api/v1/approvals/"+req1.ID, nil), "ws-b")
	rr := doRequest(t, srv, req)

	// GetCheckWorkspace returns (nil, false) — so server returns 404
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 cross-workspace approval access, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestWorkspaceIsolation_ApprovalDecideCrossWorkspaceForbidden verifies cross-workspace
// approval decisions are rejected when isolation is enabled.
func TestWorkspaceIsolation_ApprovalDecideCrossWorkspaceForbidden(t *testing.T) {
	srv := newIsolatedTestServer(t)

	reqA, err := srv.approvalQueue.SubmitWithWorkspace("ws-a", "probe-a", nil, "reason", "high", "test", "queue", nil)
	if err != nil {
		t.Fatal(err)
	}

	req := withWorkspaceAPIKey(
		httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+reqA.ID+"/decide", strings.NewReader(`{"decision":"denied","decided_by":"operator"}`)),
		"ws-b",
	)
	rr := doRequest(t, srv, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 cross-workspace approval decide, got %d: %s", rr.Code, rr.Body.String())
	}
}
