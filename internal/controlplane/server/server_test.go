package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	t.Setenv("LEGATOR_LLM_PROVIDER", "")
	t.Setenv("LEGATOR_AUTH", "0")
	t.Setenv("LEGATOR_SIGNING_KEY", strings.Repeat("a", 64))

	cfg := config.Config{
		ListenAddr: ":0",
		DataDir:    t.TempDir(),
	}
	logger := zap.NewNop()
	srv, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func connectProbeWS(t *testing.T, srv *Server, probeID string) (*websocket.Conn, func()) {
	t.Helper()

	// Ensure probe has an API key for WebSocket auth
	const testAPIKey = "test-probe-api-key"
	_ = srv.fleetMgr.SetAPIKey(probeID, testAPIKey)

	ts := httptest.NewServer(http.HandlerFunc(srv.hub.HandleProbeWS))
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/probe?id=" + url.QueryEscape(probeID)

	header := http.Header{"Authorization": []string{"Bearer " + testAPIKey}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		ts.Close()
		t.Fatalf("dial probe ws: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		ts.Close()
	}
	return conn, cleanup
}

func TestNew_InitializesCoreComponents(t *testing.T) {
	srv := newTestServer(t)

	if srv.fleetMgr == nil {
		t.Fatal("fleet manager not initialized")
	}
	if srv.tokenStore == nil {
		t.Fatal("token store not initialized")
	}
	if srv.cmdTracker == nil {
		t.Fatal("command tracker not initialized")
	}
	if srv.approvalQueue == nil {
		t.Fatal("approval queue not initialized")
	}
	if srv.hub == nil {
		t.Fatal("websocket hub not initialized")
	}
	if srv.eventBus == nil {
		t.Fatal("event bus not initialized")
	}
	if srv.policyStore == nil {
		t.Fatal("policy store not initialized")
	}
	if srv.httpServer == nil {
		t.Fatal("http server not initialized")
	}
}

func TestHandleHealthz(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	srv.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "ok") {
		t.Fatalf("expected body to contain ok, got %q", rr.Body.String())
	}
}

func TestHandleVersion(t *testing.T) {
	srv := newTestServer(t)

	oldVersion, oldCommit, oldDate := Version, Commit, Date
	Version, Commit, Date = "v1.2.3-test", "abc123", "2026-02-26"
	defer func() {
		Version, Commit, Date = oldVersion, oldCommit, oldDate
	}()

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rr := httptest.NewRecorder()

	srv.handleVersion(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	if got["version"] != "v1.2.3-test" || got["commit"] != "abc123" || got["date"] != "2026-02-26" {
		t.Fatalf("unexpected version payload: %#v", got)
	}
}

func TestHandleListProbes(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-a", "host-a", "linux", "amd64")
	srv.fleetMgr.Register("probe-b", "host-b", "linux", "arm64")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes", nil)
	rr := httptest.NewRecorder()

	srv.handleListProbes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var got []*fleet.ProbeState
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode probes response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 probes, got %d", len(got))
	}
}

func TestHandleGetProbe_Success(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-get", "host-get", "linux", "amd64")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes/probe-get", nil)
	req.SetPathValue("id", "probe-get")
	rr := httptest.NewRecorder()

	srv.handleGetProbe(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got fleet.ProbeState
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode probe response: %v", err)
	}
	if got.ID != "probe-get" || got.Hostname != "host-get" {
		t.Fatalf("unexpected probe response: %#v", got)
	}
}

func TestHandleGetProbe_NotFound(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes/missing", nil)
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()

	srv.handleGetProbe(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestHandleProbeHealth_DefaultAndNotFound(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-health", "host", "linux", "amd64")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes/probe-health/health", nil)
	req.SetPathValue("id", "probe-health")
	rr := httptest.NewRecorder()

	srv.handleProbeHealth(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var score fleet.HealthScore
	if err := json.NewDecoder(rr.Body).Decode(&score); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if score.Status != "unknown" || score.Score != 0 {
		t.Fatalf("expected unknown score=0, got %+v", score)
	}

	reqMissing := httptest.NewRequest(http.MethodGet, "/api/v1/probes/missing/health", nil)
	reqMissing.SetPathValue("id", "missing")
	rrMissing := httptest.NewRecorder()

	srv.handleProbeHealth(rrMissing, reqMissing)

	if rrMissing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing probe, got %d", rrMissing.Code)
	}
}

func TestHandleDispatchCommand_InvalidBody(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-cmd", "host", "linux", "amd64")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-cmd/command", strings.NewReader("{"))
	req.SetPathValue("id", "probe-cmd")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestHandleDispatchCommand_PendingApproval(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-approval", "host", "linux", "amd64")

	body := map[string]any{
		"request_id": "req-approval",
		"command":    "systemctl restart nginx",
		"level":      string(protocol.CapRemediate),
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-approval/command", bytes.NewReader(data))
	req.SetPathValue("id", "probe-approval")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode approval response: %v", err)
	}
	if got["status"] != "pending_approval" {
		t.Fatalf("expected pending_approval, got %v", got["status"])
	}
	approvalID, _ := got["approval_id"].(string)
	if approvalID == "" {
		t.Fatalf("expected approval_id in response: %#v", got)
	}
	if srv.approvalQueue.PendingCount() != 1 {
		t.Fatalf("expected 1 pending approval, got %d", srv.approvalQueue.PendingCount())
	}
}

func TestHandleDispatchCommand_DispatchErrorWhenProbeDisconnected(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-offline", "host", "linux", "amd64")

	body := protocol.CommandPayload{
		RequestID: "req-disconnected",
		Command:   "ls",
		Level:     protocol.CapObserve,
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-offline/command", bytes.NewReader(data))
	req.SetPathValue("id", "probe-offline")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not connected") {
		t.Fatalf("expected not connected error, got %s", rr.Body.String())
	}
}

func TestHandleDispatchCommand_WaitAndStreamMode(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-live", "host", "linux", "amd64")

	conn, cleanup := connectProbeWS(t, srv, "probe-live")
	defer cleanup()

	errCh := make(chan error, 1)
	go func() {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read command envelope: %w", err)
			return
		}

		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			errCh <- fmt.Errorf("decode command envelope: %w", err)
			return
		}
		if env.Type != protocol.MsgCommand {
			errCh <- fmt.Errorf("expected message type command, got %s", env.Type)
			return
		}

		payloadBytes, err := json.Marshal(env.Payload)
		if err != nil {
			errCh <- fmt.Errorf("marshal envelope payload: %w", err)
			return
		}

		var cmd protocol.CommandPayload
		if err := json.Unmarshal(payloadBytes, &cmd); err != nil {
			errCh <- fmt.Errorf("decode command payload: %w", err)
			return
		}
		if !cmd.Stream {
			errCh <- fmt.Errorf("expected stream=true command")
			return
		}

		result := protocol.CommandResultPayload{
			RequestID: cmd.RequestID,
			ExitCode:  0,
			Stdout:    "command ok",
			Duration:  12,
		}
		resp := protocol.Envelope{
			ID:        "response-1",
			Type:      protocol.MsgCommandResult,
			Timestamp: time.Now().UTC(),
			Payload:   result,
		}
		if err := conn.WriteJSON(resp); err != nil {
			errCh <- fmt.Errorf("write command result: %w", err)
			return
		}

		errCh <- nil
	}()

	body := protocol.CommandPayload{
		RequestID: "req-wait-stream",
		Command:   "ls",
		Level:     protocol.CapObserve,
		Timeout:   2 * time.Second,
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-live/command?wait=true&stream=true", bytes.NewReader(data))
	req.SetPathValue("id", "probe-live")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got protocol.CommandResultPayload
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode wait response: %v", err)
	}
	if got.RequestID != "req-wait-stream" || got.ExitCode != 0 || got.Stdout != "command ok" {
		t.Fatalf("unexpected command result: %+v", got)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for probe websocket flow")
	}
}

func TestHandleSetTags(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-tags", "host", "linux", "amd64")

	body := `{"tags":["Prod"," db ","prod",""]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/probes/probe-tags/tags", strings.NewReader(body))
	req.SetPathValue("id", "probe-tags")
	rr := httptest.NewRecorder()

	srv.handleSetTags(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	ps, ok := srv.fleetMgr.Get("probe-tags")
	if !ok {
		t.Fatal("probe missing after set tags")
	}
	if len(ps.Tags) != 2 || ps.Tags[0] != "prod" || ps.Tags[1] != "db" {
		t.Fatalf("unexpected tags: %#v", ps.Tags)
	}

	badReq := httptest.NewRequest(http.MethodPut, "/api/v1/probes/probe-tags/tags", strings.NewReader("{"))
	badReq.SetPathValue("id", "probe-tags")
	badRR := httptest.NewRecorder()
	srv.handleSetTags(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid body, got %d", badRR.Code)
	}

	notFoundReq := httptest.NewRequest(http.MethodPut, "/api/v1/probes/missing/tags", strings.NewReader(`{"tags":["x"]}`))
	notFoundReq.SetPathValue("id", "missing")
	notFoundRR := httptest.NewRecorder()
	srv.handleSetTags(notFoundRR, notFoundReq)
	if notFoundRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing probe, got %d", notFoundRR.Code)
	}
}

func TestHandleFleetSummary(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-1", "host-1", "linux", "amd64")
	srv.fleetMgr.Register("probe-2", "host-2", "linux", "amd64")

	ps, _ := srv.fleetMgr.Get("probe-2")
	ps.Status = "offline"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/summary", nil)
	rr := httptest.NewRecorder()
	srv.handleFleetSummary(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode fleet summary: %v", err)
	}

	counts, ok := got["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts missing from summary: %#v", got)
	}
	if counts["online"] != float64(1) || counts["offline"] != float64(1) {
		t.Fatalf("unexpected counts: %#v", counts)
	}
	if got["pending_approvals"] != float64(0) {
		t.Fatalf("unexpected pending approvals: %#v", got)
	}
}

func TestHandleDeleteProbe(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-delete", "host", "linux", "amd64")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/probes/probe-delete", nil)
	req.SetPathValue("id", "probe-delete")
	rr := httptest.NewRecorder()

	srv.handleDeleteProbe(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"deleted":"probe-delete"`) {
		t.Fatalf("unexpected delete response: %s", rr.Body.String())
	}
	if _, ok := srv.fleetMgr.Get("probe-delete"); ok {
		t.Fatal("probe should be deleted")
	}

	events := srv.queryAudit(audit.Filter{ProbeID: "probe-delete", Type: audit.EventProbeDeregistered, Limit: 1})
	if len(events) != 1 {
		t.Fatalf("expected probe.deregistered audit event, got %d", len(events))
	}
}

func TestHandleDeleteProbe_NotFound(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/probes/missing", nil)
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()

	srv.handleDeleteProbe(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestHandleFleetCleanup(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-old", "host-old", "linux", "amd64")
	srv.fleetMgr.Register("probe-recent", "host-recent", "linux", "amd64")
	srv.fleetMgr.Register("probe-online", "host-online", "linux", "amd64")

	old, _ := srv.fleetMgr.Get("probe-old")
	old.Status = "offline"
	old.LastSeen = time.Now().UTC().Add(-2 * time.Hour)

	recent, _ := srv.fleetMgr.Get("probe-recent")
	recent.Status = "offline"
	recent.LastSeen = time.Now().UTC().Add(-10 * time.Minute)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/cleanup?older_than=1h", nil)
	rr := httptest.NewRecorder()

	srv.handleFleetCleanup(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode cleanup response: %v", err)
	}
	if got["count"] != float64(1) {
		t.Fatalf("expected count=1, got %#v", got)
	}

	if _, ok := srv.fleetMgr.Get("probe-old"); ok {
		t.Fatal("expected old probe to be removed")
	}
	if _, ok := srv.fleetMgr.Get("probe-recent"); !ok {
		t.Fatal("recent offline probe should not be removed")
	}
}

func TestHandleListApprovals(t *testing.T) {
	srv := newTestServer(t)

	_, err := srv.approvalQueue.Submit("probe-1", &protocol.CommandPayload{RequestID: "req-1", Command: "rm -rf /tmp/x"}, "reason", "critical", "api")
	if err != nil {
		t.Fatalf("submit approval 1: %v", err)
	}
	_, err = srv.approvalQueue.Submit("probe-2", &protocol.CommandPayload{RequestID: "req-2", Command: "systemctl restart nginx"}, "reason", "high", "api")
	if err != nil {
		t.Fatalf("submit approval 2: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals?status=pending", nil)
	rr := httptest.NewRecorder()
	srv.handleListApprovals(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got struct {
		Approvals    []approval.Request `json:"approvals"`
		PendingCount int                `json:"pending_count"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode approvals response: %v", err)
	}
	if got.PendingCount != 2 || len(got.Approvals) != 2 {
		t.Fatalf("unexpected approvals payload: %+v", got)
	}
}

func TestHandleDecideApproval(t *testing.T) {
	srv := newTestServer(t)

	req, err := srv.approvalQueue.Submit(
		"probe-decide",
		&protocol.CommandPayload{RequestID: "req-decide", Command: "systemctl restart nginx"},
		"manual",
		"high",
		"api",
	)
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+req.ID+"/decide", strings.NewReader(`{"decision":"denied"}`))
	badReq.SetPathValue("id", req.ID)
	badRR := httptest.NewRecorder()
	srv.handleDecideApproval(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing decided_by, got %d", badRR.Code)
	}

	goodReq := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+req.ID+"/decide", strings.NewReader(`{"decision":"denied","decided_by":"operator"}`))
	goodReq.SetPathValue("id", req.ID)
	goodRR := httptest.NewRecorder()
	srv.handleDecideApproval(goodRR, goodReq)

	if goodRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", goodRR.Code, goodRR.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(goodRR.Body).Decode(&got); err != nil {
		t.Fatalf("decode decide response: %v", err)
	}
	if got["status"] != string(approval.DecisionDenied) {
		t.Fatalf("expected denied status, got %#v", got)
	}

	updated, ok := srv.approvalQueue.Get(req.ID)
	if !ok {
		t.Fatalf("approval %s missing after decision", req.ID)
	}
	if updated.Decision != approval.DecisionDenied {
		t.Fatalf("expected denied decision in queue, got %s", updated.Decision)
	}
}

func TestHandleAuditLog(t *testing.T) {
	srv := newTestServer(t)
	srv.emitAudit(audit.EventCommandSent, "probe-a", "api", "command a")
	srv.emitAudit(audit.EventCommandSent, "probe-b", "api", "command b")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?probe_id=probe-a&limit=1", nil)
	rr := httptest.NewRecorder()

	srv.handleAuditLog(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var got struct {
		Events []audit.Event `json:"events"`
		Total  int           `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode audit response: %v", err)
	}
	if got.Total != 2 {
		t.Fatalf("expected total=2, got %d", got.Total)
	}
	if len(got.Events) != 1 || got.Events[0].ProbeID != "probe-a" {
		t.Fatalf("unexpected events payload: %+v", got.Events)
	}
}

func TestPolicyHandlers_ListCreateDelete(t *testing.T) {
	srv := newTestServer(t)

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	listRR := httptest.NewRecorder()
	srv.handleListPolicies(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200 listing policies, got %d", listRR.Code)
	}

	var listed []policy.Template
	if err := json.NewDecoder(listRR.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list policies: %v", err)
	}
	if len(listed) < 3 {
		t.Fatalf("expected built-in policies, got %d", len(listed))
	}

	createBody := `{"name":"Staging","description":"staging policy","level":"diagnose","allowed":["ls"],"blocked":["rm"],"paths":["/tmp"]}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader(createBody))
	createRR := httptest.NewRecorder()
	srv.handleCreatePolicy(createRR, createReq)

	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating policy, got %d: %s", createRR.Code, createRR.Body.String())
	}

	var created policy.Template
	if err := json.NewDecoder(createRR.Body).Decode(&created); err != nil {
		t.Fatalf("decode created policy: %v", err)
	}
	if created.ID == "" || created.Name != "Staging" {
		t.Fatalf("unexpected created policy: %+v", created)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/policies/"+created.ID, nil)
	deleteReq.SetPathValue("id", created.ID)
	deleteRR := httptest.NewRecorder()
	srv.handleDeletePolicy(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting policy, got %d", deleteRR.Code)
	}

	if _, ok := srv.policyStore.Get(created.ID); ok {
		t.Fatalf("policy %s should be deleted", created.ID)
	}

	missingReq := httptest.NewRequest(http.MethodDelete, "/api/v1/policies/missing", nil)
	missingReq.SetPathValue("id", "missing")
	missingRR := httptest.NewRecorder()
	srv.handleDeletePolicy(missingRR, missingReq)
	if missingRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 deleting missing policy, got %d", missingRR.Code)
	}
}

func TestHandleTask_NoProviderConfigured(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/p1/task", strings.NewReader(`{"task":"check disk"}`))
	req.SetPathValue("id", "p1")
	rr := httptest.NewRecorder()

	srv.handleTask(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no LLM provider, got %d", rr.Code)
	}
}

func TestHandleEventsSSE_BasicConnectivity(t *testing.T) {
	srv := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		srv.handleEventsSSE(rr, req)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(rr.Body.String(), ": connected") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("did not receive SSE keepalive")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("events handler did not exit after context cancel")
	}

	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("expected Cache-Control no-cache, got %q", cc)
	}
}
