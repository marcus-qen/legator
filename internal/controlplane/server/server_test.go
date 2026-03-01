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
	"github.com/marcus-qen/legator/internal/controlplane/chat"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/controlplane/reliability"
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

type testCapacityProvider struct {
	signals *coreapprovalpolicy.CapacitySignals
	err     error
}

func (p testCapacityProvider) CapacitySignals(context.Context) (*coreapprovalpolicy.CapacitySignals, error) {
	if p.signals == nil {
		return nil, p.err
	}
	clone := *p.signals
	clone.Warnings = append([]string(nil), p.signals.Warnings...)
	return &clone, p.err
}

type fakeFederationSourceAdapter struct {
	source  fleet.FederationSourceDescriptor
	result  fleet.FederationSourceResult
	err     error
	filters []fleet.InventoryFilter
}

func (f *fakeFederationSourceAdapter) Source() fleet.FederationSourceDescriptor {
	return f.source
}

func (f *fakeFederationSourceAdapter) Inventory(_ context.Context, filter fleet.InventoryFilter) (fleet.FederationSourceResult, error) {
	f.filters = append(f.filters, filter)
	if f.err != nil {
		return fleet.FederationSourceResult{}, f.err
	}
	return f.result, nil
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
	if srv.federationStore == nil {
		t.Fatal("federation store not initialized")
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
	if srv.approvalCore == nil {
		t.Fatal("approval core not initialized")
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

func TestHandleJobLifecycleEventPublishesAuditAndEventBus(t *testing.T) {
	srv := newTestServer(t)
	const subID = "job-lifecycle-test"
	ch := srv.eventBus.Subscribe(subID)
	defer srv.eventBus.Unsubscribe(subID)

	event := jobs.LifecycleEvent{
		Type:        jobs.EventJobRunQueued,
		Actor:       "scheduler",
		Timestamp:   time.Now().UTC(),
		JobID:       "job-123",
		RunID:       "run-123",
		ExecutionID: "exec-123",
		ProbeID:     "probe-123",
		Attempt:     1,
		MaxAttempts: 3,
		RequestID:   "req-123",
	}
	srv.handleJobLifecycleEvent(event)

	select {
	case got := <-ch:
		if got.Type != events.JobRunQueued {
			t.Fatalf("expected event type %s, got %s", events.JobRunQueued, got.Type)
		}
		detail, ok := got.Detail.(map[string]any)
		if !ok {
			t.Fatalf("expected detail map, got %T", got.Detail)
		}
		if detail["job_id"] != "job-123" || detail["request_id"] != "req-123" {
			t.Fatalf("unexpected event detail: %+v", detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected lifecycle event on event bus")
	}

	auditEvents := srv.queryAudit(audit.Filter{Type: audit.EventJobRunQueued, Limit: 5})
	if len(auditEvents) == 0 {
		t.Fatal("expected job lifecycle audit event")
	}
	if auditEvents[0].Actor != "scheduler" {
		t.Fatalf("expected actor scheduler, got %q", auditEvents[0].Actor)
	}
	detail, ok := auditEvents[0].Detail.(map[string]any)
	if !ok {
		t.Fatalf("expected audit detail map, got %T", auditEvents[0].Detail)
	}
	if detail["run_id"] != "run-123" || detail["execution_id"] != "exec-123" {
		t.Fatalf("unexpected audit detail: %+v", detail)
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
	if got["policy_decision"] != "queue" {
		t.Fatalf("expected policy_decision=queue, got %v", got["policy_decision"])
	}
	if _, ok := got["policy_rationale"].(map[string]any); !ok {
		t.Fatalf("expected policy_rationale map, got %#v", got["policy_rationale"])
	}
	if srv.approvalQueue.PendingCount() != 1 {
		t.Fatalf("expected 1 pending approval, got %d", srv.approvalQueue.PendingCount())
	}

	queued, ok := srv.approvalQueue.Get(approvalID)
	if !ok {
		t.Fatalf("expected queued approval %q to exist", approvalID)
	}
	if queued.PolicyDecision != "queue" {
		t.Fatalf("expected queued policy_decision=queue, got %q", queued.PolicyDecision)
	}
	if queued.PolicyRationale == nil {
		t.Fatal("expected queued approval policy_rationale")
	}
}

func TestHandleDispatchCommand_CapacityDenied(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-capacity", "host", "linux", "amd64")
	srv.approvalCore = coreapprovalpolicy.NewService(
		srv.approvalQueue,
		srv.fleetMgr,
		srv.policyStore,
		coreapprovalpolicy.WithCapacitySignalProvider(testCapacityProvider{signals: &coreapprovalpolicy.CapacitySignals{
			Source:            "grafana",
			Availability:      "degraded",
			DashboardCoverage: 0.9,
			QueryCoverage:     0.9,
			DatasourceCount:   2,
		}}),
	)

	body := map[string]any{
		"request_id": "req-capacity-deny",
		"command":    "ls",
		"level":      string(protocol.CapObserve),
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-capacity/command", bytes.NewReader(data))
	req.SetPathValue("id", "probe-capacity")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode capacity deny response: %v", err)
	}
	if got["status"] != "denied" {
		t.Fatalf("expected status=denied, got %v", got["status"])
	}
	if got["policy_decision"] != "deny" {
		t.Fatalf("expected policy_decision=deny, got %v", got["policy_decision"])
	}
	rationale, ok := got["policy_rationale"].(map[string]any)
	if !ok {
		t.Fatalf("expected policy_rationale payload, got %#v", got["policy_rationale"])
	}
	if rationale["fallback"] != false {
		t.Fatalf("expected fallback=false rationale, got %v", rationale["fallback"])
	}
	if srv.approvalQueue.PendingCount() != 0 {
		t.Fatalf("expected no queued approvals on deny, got %d", srv.approvalQueue.PendingCount())
	}
}

func TestHandleDispatchCommand_GrafanaUnavailableFallbackDoesNotDeny(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-fallback", "host", "linux", "amd64")
	srv.approvalCore = coreapprovalpolicy.NewService(
		srv.approvalQueue,
		srv.fleetMgr,
		srv.policyStore,
		coreapprovalpolicy.WithCapacitySignalProvider(testCapacityProvider{err: fmt.Errorf("grafana unavailable")}),
	)

	body := map[string]any{
		"request_id": "req-capacity-fallback",
		"command":    "ls",
		"level":      string(protocol.CapObserve),
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-fallback/command", bytes.NewReader(data))
	req.SetPathValue("id", "probe-fallback")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 dispatch attempt (fallback allow), got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "capacity policy") {
		t.Fatalf("expected no capacity denial in fallback mode, got %s", rr.Body.String())
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

func TestHandleDispatchCommand_WaitTimeout(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-timeout", "host", "linux", "amd64")

	conn, cleanup := connectProbeWS(t, srv, "probe-timeout")
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

		errCh <- nil
	}()

	body := protocol.CommandPayload{
		RequestID: "req-wait-timeout",
		Command:   "ls",
		Level:     protocol.CapObserve,
		Timeout:   1 * time.Millisecond,
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-timeout/command?wait=true", bytes.NewReader(data))
	req.SetPathValue("id", "probe-timeout")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d: %s", rr.Code, rr.Body.String())
	}

	var apiErr APIError
	if err := json.NewDecoder(rr.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decode timeout error body: %v", err)
	}
	if apiErr.Code != "timeout" || apiErr.Error != "timeout waiting for probe response" {
		t.Fatalf("unexpected timeout payload: %+v", apiErr)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for probe websocket command dispatch")
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
	reliabilityPayload, ok := got["reliability"].(map[string]any)
	if !ok {
		t.Fatalf("expected reliability payload in fleet summary: %#v", got)
	}
	if _, ok := reliabilityPayload["overall"].(map[string]any); !ok {
		t.Fatalf("expected reliability.overall payload, got %#v", reliabilityPayload)
	}
}

func TestHandleReliabilityScorecard(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-online", "host-1", "linux", "amd64")
	srv.fleetMgr.Register("probe-offline", "host-2", "linux", "amd64")
	ps, _ := srv.fleetMgr.Get("probe-offline")
	ps.Status = "offline"

	srv.recordAudit(audit.Event{Type: audit.EventCommandResult, ProbeID: "probe-online", Detail: map[string]any{"exit_code": 0}})
	srv.recordAudit(audit.Event{Type: audit.EventCommandResult, ProbeID: "probe-online", Detail: map[string]any{"exit_code": 1}})

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRR := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(healthRR, healthReq)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/reliability/scorecard?window=15m", nil)
	rr := httptest.NewRecorder()
	srv.handleReliabilityScorecard(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload reliability.Scorecard
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode reliability scorecard: %v", err)
	}
	if payload.Window.Duration != "15m0s" {
		t.Fatalf("expected 15m window metadata, got %q", payload.Window.Duration)
	}
	if len(payload.Surfaces) != 2 {
		t.Fatalf("expected 2 surfaces, got %d", len(payload.Surfaces))
	}

	var foundCommandIndicator bool
	for _, surface := range payload.Surfaces {
		for _, indicator := range surface.Indicators {
			if indicator.ID != "probe_fleet.command_success" {
				continue
			}
			foundCommandIndicator = true
			if indicator.Objective.Comparator != "gte" {
				t.Fatalf("expected comparator metadata for command success, got %+v", indicator.Objective)
			}
			if indicator.Metric.SampleSize != 2 {
				t.Fatalf("expected command sample size=2, got %d", indicator.Metric.SampleSize)
			}
		}
	}
	if !foundCommandIndicator {
		t.Fatal("expected probe_fleet.command_success indicator in scorecard")
	}
}

func TestHandleReliabilityScorecard_InvalidWindow(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reliability/scorecard?window=not-a-duration", nil)
	rr := httptest.NewRecorder()
	srv.handleReliabilityScorecard(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid window, got %d", rr.Code)
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

	_, err := srv.approvalQueue.SubmitWithPolicyDetails(
		"probe-1",
		&protocol.CommandPayload{RequestID: "req-1", Command: "rm -rf /tmp/x"},
		"reason",
		"critical",
		"api",
		"queue",
		map[string]any{"summary": "queue (high-risk command requires human approval)", "indicators": []map[string]any{{"name": "command_risk", "drove_outcome": true}}},
	)
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

	foundExplainability := false
	for _, item := range got.Approvals {
		if item.PolicyDecision == "queue" && item.PolicyRationale != nil {
			foundExplainability = true
			break
		}
	}
	if !foundExplainability {
		t.Fatalf("expected at least one approval with policy explainability fields: %+v", got.Approvals)
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

	subID := "test-decide-" + req.ID
	eventsCh := srv.eventBus.Subscribe(subID)
	defer srv.eventBus.Unsubscribe(subID)

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

	select {
	case evt := <-eventsCh:
		if evt.Type != events.ApprovalDecided {
			t.Fatalf("expected approval.decided event, got %s", evt.Type)
		}
		if evt.ProbeID != "probe-decide" {
			t.Fatalf("expected probe-decide event probe id, got %s", evt.ProbeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected approval.decided event")
	}

	auditEvents := srv.queryAudit(audit.Filter{ProbeID: "probe-decide", Limit: 20})
	hasApprovalAudit := false
	hasCommandAudit := false
	for _, evt := range auditEvents {
		if evt.Type == audit.EventApprovalDecided {
			hasApprovalAudit = true
		}
		if evt.Type == audit.EventCommandSent {
			hasCommandAudit = true
		}
	}
	if !hasApprovalAudit {
		t.Fatal("expected approval decision audit event")
	}
	if hasCommandAudit {
		t.Fatal("did not expect command sent audit event for denied decision")
	}
}

func TestHandleDecideApproval_SuccessContractParityDenied(t *testing.T) {
	srv := newTestServer(t)

	req, err := srv.approvalQueue.Submit(
		"probe-decide-denied-parity",
		&protocol.CommandPayload{RequestID: "req-decide-denied-parity", Command: "systemctl restart nginx"},
		"manual",
		"high",
		"api",
	)
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+req.ID+"/decide", strings.NewReader(`{"decision":"denied","decided_by":"operator"}`))
	httpReq.SetPathValue("id", req.ID)
	rr := httptest.NewRecorder()
	srv.handleDecideApproval(rr, httpReq)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode decide response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 top-level fields {status,request}, got %#v", got)
	}
	if got["status"] != string(approval.DecisionDenied) {
		t.Fatalf("expected denied status, got %#v", got)
	}

	request, ok := got["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected object request payload, got %#v", got["request"])
	}
	if request["id"] != req.ID {
		t.Fatalf("expected request id %q, got %#v", req.ID, request["id"])
	}
	if request["decision"] != string(approval.DecisionDenied) {
		t.Fatalf("expected request decision denied, got %#v", request["decision"])
	}
	if _, ok := request["command"].(map[string]any); !ok {
		t.Fatalf("expected request.command object, got %#v", request["command"])
	}
}

func TestHandleDecideApproval_SuccessContractParityApproved(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-decide-approved-parity", "host", "linux", "amd64")

	conn, cleanup := connectProbeWS(t, srv, "probe-decide-approved-parity")
	defer cleanup()

	probeErr := make(chan error, 1)
	go func() {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			probeErr <- fmt.Errorf("read command envelope: %w", err)
			return
		}

		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			probeErr <- fmt.Errorf("decode command envelope: %w", err)
			return
		}
		if env.Type != protocol.MsgCommand {
			probeErr <- fmt.Errorf("expected command message, got %s", env.Type)
			return
		}

		probeErr <- nil
	}()

	req, err := srv.approvalQueue.Submit(
		"probe-decide-approved-parity",
		&protocol.CommandPayload{RequestID: "req-decide-approved-parity", Command: "systemctl restart nginx"},
		"manual",
		"high",
		"api",
	)
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+req.ID+"/decide", strings.NewReader(`{"decision":"approved","decided_by":"operator"}`))
	httpReq.SetPathValue("id", req.ID)
	rr := httptest.NewRecorder()
	srv.handleDecideApproval(rr, httpReq)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode decide response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 top-level fields {status,request}, got %#v", got)
	}
	if got["status"] != string(approval.DecisionApproved) {
		t.Fatalf("expected approved status, got %#v", got)
	}

	request, ok := got["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected object request payload, got %#v", got["request"])
	}
	if request["id"] != req.ID {
		t.Fatalf("expected request id %q, got %#v", req.ID, request["id"])
	}
	if request["decision"] != string(approval.DecisionApproved) {
		t.Fatalf("expected request decision approved, got %#v", request["decision"])
	}
	if _, ok := request["command"].(map[string]any); !ok {
		t.Fatalf("expected request.command object, got %#v", request["command"])
	}

	select {
	case err := <-probeErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for approved dispatch command")
	}
}

func TestHandleDecideApproval_InvalidDecisionErrorMapping(t *testing.T) {
	srv := newTestServer(t)

	req, err := srv.approvalQueue.Submit(
		"probe-decide-invalid",
		&protocol.CommandPayload{RequestID: "req-decide-invalid", Command: "systemctl restart nginx"},
		"manual",
		"high",
		"api",
	)
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+req.ID+"/decide", strings.NewReader(`{"decision":"maybe","decided_by":"operator"}`))
	httpReq.SetPathValue("id", req.ID)
	rr := httptest.NewRecorder()
	srv.handleDecideApproval(rr, httpReq)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	var apiErr APIError
	if err := json.NewDecoder(rr.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if apiErr.Code != "invalid_request" {
		t.Fatalf("expected invalid_request code, got %q", apiErr.Code)
	}
	if apiErr.Error != "invalid decision \"maybe\": must be approved or denied" {
		t.Fatalf("unexpected invalid decision message: %q", apiErr.Error)
	}
}

func TestHandleDecideApproval_ApprovedDispatchFailure(t *testing.T) {
	srv := newTestServer(t)

	req, err := srv.approvalQueue.Submit(
		"probe-decide-fail",
		&protocol.CommandPayload{RequestID: "req-decide-fail", Command: "systemctl restart nginx"},
		"manual",
		"high",
		"api",
	)
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	subID := "test-decide-fail-" + req.ID
	eventsCh := srv.eventBus.Subscribe(subID)
	defer srv.eventBus.Unsubscribe(subID)

	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+req.ID+"/decide", strings.NewReader(`{"decision":"approved","decided_by":"operator"}`))
	httpReq.SetPathValue("id", req.ID)
	rr := httptest.NewRecorder()
	srv.handleDecideApproval(rr, httpReq)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}

	var apiErr APIError
	if err := json.NewDecoder(rr.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if apiErr.Code != "bad_gateway" {
		t.Fatalf("expected bad_gateway code, got %q", apiErr.Code)
	}
	if apiErr.Error != "approved but dispatch failed: probe probe-decide-fail not connected" {
		t.Fatalf("expected preserved dispatch failure wording, got %q", apiErr.Error)
	}

	updated, ok := srv.approvalQueue.Get(req.ID)
	if !ok {
		t.Fatalf("approval %s missing after decision", req.ID)
	}
	if updated.Decision != approval.DecisionApproved {
		t.Fatalf("expected approved decision in queue, got %s", updated.Decision)
	}

	select {
	case evt := <-eventsCh:
		if evt.Type != events.ApprovalDecided {
			t.Fatalf("expected approval.decided event, got %s", evt.Type)
		}
		if evt.ProbeID != "probe-decide-fail" {
			t.Fatalf("expected probe-decide-fail event probe id, got %s", evt.ProbeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected approval.decided event")
	}

	auditEvents := srv.queryAudit(audit.Filter{ProbeID: "probe-decide-fail", Limit: 20})
	hasApprovalAudit := false
	hasCommandAudit := false
	for _, evt := range auditEvents {
		if evt.Type == audit.EventApprovalDecided {
			hasApprovalAudit = true
		}
		if evt.Type == audit.EventCommandSent {
			hasCommandAudit = true
		}
	}
	if !hasApprovalAudit {
		t.Fatal("expected approval decision audit event")
	}
	if hasCommandAudit {
		t.Fatal("did not expect command sent audit event when approved dispatch fails")
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

func TestOIDCDisabledRoutesNotRegisteredAndLoginUnchanged(t *testing.T) {
	t.Setenv("LEGATOR_LLM_PROVIDER", "")
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
	defer srv.Close()

	oidcReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil)
	oidcRec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(oidcRec, oidcReq)
	if oidcRec.Code != http.StatusNotFound {
		t.Fatalf("expected /auth/oidc/login to be unregistered (404), got %d", oidcRec.Code)
	}

}

func TestHandleFleetInventory_WithFilters(t *testing.T) {
	srv := newTestServer(t)

	srv.fleetMgr.Register("probe-1", "web-01", "linux", "amd64")
	srv.fleetMgr.Register("probe-2", "db-01", "linux", "amd64")

	_ = srv.fleetMgr.SetTags("probe-1", []string{"prod", "k8s-host"})
	_ = srv.fleetMgr.SetTags("probe-2", []string{"prod"})
	_ = srv.fleetMgr.UpdateInventory("probe-1", &protocol.InventoryPayload{CPUs: 4, MemTotal: 8 * 1024 * 1024 * 1024, OS: "linux"})
	_ = srv.fleetMgr.UpdateInventory("probe-2", &protocol.InventoryPayload{CPUs: 2, MemTotal: 4 * 1024 * 1024 * 1024, OS: "linux"})

	ps, ok := srv.fleetMgr.Get("probe-2")
	if !ok {
		t.Fatal("expected probe-2 to exist")
	}
	ps.Status = "offline"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/inventory?tag=prod&status=online", nil)
	rr := httptest.NewRecorder()

	srv.handleFleetInventory(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload fleet.FleetInventory
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(payload.Probes) != 1 || payload.Probes[0].ID != "probe-1" {
		t.Fatalf("unexpected probe list: %#v", payload.Probes)
	}
	if payload.Aggregates.TotalProbes != 1 || payload.Aggregates.Online != 1 {
		t.Fatalf("unexpected aggregates: %#v", payload.Aggregates)
	}
	if payload.Aggregates.TotalCPUs != 4 {
		t.Fatalf("expected 4 CPUs, got %d", payload.Aggregates.TotalCPUs)
	}
	if payload.Aggregates.TotalRAMBytes != 8*1024*1024*1024 {
		t.Fatalf("unexpected total RAM: %d", payload.Aggregates.TotalRAMBytes)
	}
}

func TestHandleFederationInventory_WithFilters(t *testing.T) {
	srv := newTestServer(t)

	srv.fleetMgr.Register("probe-1", "web-01", "linux", "amd64")
	srv.fleetMgr.Register("probe-2", "db-01", "linux", "amd64")
	_ = srv.fleetMgr.SetTags("probe-1", []string{"prod"})
	_ = srv.fleetMgr.SetTags("probe-2", []string{"dev"})
	_ = srv.fleetMgr.UpdateInventory("probe-1", &protocol.InventoryPayload{CPUs: 4, MemTotal: 8 * 1024 * 1024 * 1024, OS: "linux"})
	_ = srv.fleetMgr.UpdateInventory("probe-2", &protocol.InventoryPayload{CPUs: 2, MemTotal: 4 * 1024 * 1024 * 1024, OS: "linux"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/federation/inventory?tag=prod&status=online&source=local&cluster=primary&site=local&search=web", nil)
	rr := httptest.NewRecorder()

	srv.handleFederationInventory(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload fleet.FederatedInventory
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Aggregates.TotalSources != 1 {
		t.Fatalf("expected 1 source, got %d", payload.Aggregates.TotalSources)
	}
	if len(payload.Probes) != 1 || payload.Probes[0].Probe.ID != "probe-1" {
		t.Fatalf("unexpected federated probes payload: %#v", payload.Probes)
	}
	if payload.Probes[0].Source.ID != "local" {
		t.Fatalf("expected local source attribution, got %+v", payload.Probes[0].Source)
	}
	if payload.Health.Overall != fleet.FederationSourceHealthy {
		t.Fatalf("expected healthy overall rollup, got %q", payload.Health.Overall)
	}
	if payload.Aggregates.TagDistribution["prod"] != 1 {
		t.Fatalf("expected aggregated prod tag count of 1, got %+v", payload.Aggregates.TagDistribution)
	}
	if payload.Consistency.Freshness != fleet.FederationFreshnessFresh || payload.Consistency.Completeness != fleet.FederationCompletenessComplete {
		t.Fatalf("expected fresh/complete federation consistency, got %+v", payload.Consistency)
	}
	if len(payload.Sources) != 1 {
		t.Fatalf("expected one source in payload, got %+v", payload.Sources)
	}
	if payload.Sources[0].Consistency.Completeness != fleet.FederationCompletenessComplete || payload.Sources[0].Consistency.FailoverMode != fleet.FederationFailoverNone {
		t.Fatalf("expected source consistency without failover, got %+v", payload.Sources[0].Consistency)
	}
}

func TestFederationFilterFromRequest_ParsesSearch(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/federation/inventory?tag=prod&status=online&source=local&cluster=primary&site=local&search=web&tenant_id=tenant-a&org=org-a&scope=scope-a", nil)
	filter := federationFilterFromRequest(req)

	if filter.Tag != "prod" || filter.Status != "online" || filter.Source != "local" || filter.Cluster != "primary" || filter.Site != "local" || filter.Search != "web" || filter.TenantID != "tenant-a" || filter.OrgID != "org-a" || filter.ScopeID != "scope-a" {
		t.Fatalf("unexpected parsed federation filter: %+v", filter)
	}
}

func TestHandleFederationSummary_UnavailableSourceRollup(t *testing.T) {
	srv := newTestServer(t)

	unavailable := &fakeFederationSourceAdapter{
		source: fleet.FederationSourceDescriptor{ID: "remote-a", Name: "Remote A", Kind: "cluster", Cluster: "eu-west", Site: "dc-2"},
		err:    fmt.Errorf("source timed out"),
	}
	srv.federationStore.RegisterSource(unavailable)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/federation/summary?source=remote-a", nil)
	rr := httptest.NewRecorder()
	srv.handleFederationSummary(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload fleet.FederatedInventorySummary
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Aggregates.TotalSources != 1 {
		t.Fatalf("expected 1 source in summary, got %d", payload.Aggregates.TotalSources)
	}
	if payload.Aggregates.UnavailableSources != 1 {
		t.Fatalf("expected unavailable source count to be 1, got %d", payload.Aggregates.UnavailableSources)
	}
	if payload.Health.Overall != fleet.FederationSourceUnavailable {
		t.Fatalf("expected overall unavailable rollup, got %q", payload.Health.Overall)
	}
	if len(payload.Sources) != 1 || payload.Sources[0].Error == "" {
		t.Fatalf("expected source error in summary payload: %#v", payload.Sources)
	}
	if payload.Sources[0].Consistency.Completeness != fleet.FederationCompletenessUnavailable || payload.Sources[0].Consistency.FailoverMode != fleet.FederationFailoverUnavailable {
		t.Fatalf("expected unavailable consistency semantics, got %+v", payload.Sources[0].Consistency)
	}
	if payload.Consistency.Completeness != fleet.FederationCompletenessUnavailable || !payload.Consistency.PartialResults {
		t.Fatalf("expected unavailable summary consistency rollup, got %+v", payload.Consistency)
	}
	if len(unavailable.filters) != 1 {
		t.Fatalf("expected adapter to receive one query, got %d", len(unavailable.filters))
	}
}

func TestHandleFederationInventory_ConsistencyFailoverFromCachedSnapshot(t *testing.T) {
	srv := newTestServer(t)

	now := time.Date(2026, time.March, 1, 13, 0, 0, 0, time.UTC)
	adapter := &fakeFederationSourceAdapter{
		source: fleet.FederationSourceDescriptor{ID: "remote-failover", Name: "Remote Failover", Kind: "cluster", Cluster: "eu-west", Site: "dc-2"},
		result: fleet.FederationSourceResult{
			CollectedAt: now,
			Inventory: fleet.FleetInventory{Probes: []fleet.ProbeInventorySummary{{
				ID:       "probe-a",
				Hostname: "a-1",
				Status:   "online",
				OS:       "linux",
			}}},
		},
	}
	srv.federationStore.RegisterSource(adapter)
	srv.federationStore.Inventory(context.Background(), fleet.FederationFilter{Source: "remote-failover"})

	adapter.err = fmt.Errorf("source offline")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/federation/inventory?source=remote-failover", nil)
	rr := httptest.NewRecorder()
	srv.handleFederationInventory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload fleet.FederatedInventory
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Probes) != 1 {
		t.Fatalf("expected cached probe during failover, got %+v", payload.Probes)
	}
	if payload.Health.Overall != fleet.FederationSourceDegraded {
		t.Fatalf("expected degraded rollup with failover, got %+v", payload.Health)
	}
	if payload.Consistency.FailoverSources != 1 || !payload.Consistency.FailoverActive {
		t.Fatalf("expected failover rollup markers, got %+v", payload.Consistency)
	}
	if len(payload.Sources) != 1 {
		t.Fatalf("expected one source payload entry, got %+v", payload.Sources)
	}
	source := payload.Sources[0]
	if source.Consistency.FailoverMode != fleet.FederationFailoverCachedSnapshot || source.Consistency.Completeness != fleet.FederationCompletenessPartial {
		t.Fatalf("expected cached snapshot failover semantics, got %+v", source.Consistency)
	}
	if source.Error == "" {
		t.Fatalf("expected source error context retained in failover payload, got %+v", source)
	}
}

func TestHandleFleetChatSendAndGet(t *testing.T) {
	srv := newTestServer(t)

	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/chat", strings.NewReader(`{"content":"fleet hello"}`))
	postRR := httptest.NewRecorder()
	srv.handleFleetSendMessage(postRR, postReq)
	if postRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", postRR.Code, postRR.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/chat?limit=20", nil)
	getRR := httptest.NewRecorder()
	srv.handleFleetGetMessages(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRR.Code, getRR.Body.String())
	}

	var history []chat.Message
	if err := json.NewDecoder(getRR.Body).Decode(&history); err != nil {
		t.Fatalf("decode fleet chat history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least 2 chat messages, got %d", len(history))
	}

	var persisted []chat.Message
	if srv.chatStore != nil {
		persisted = srv.chatStore.GetMessages("fleet", 20)
	} else {
		persisted = srv.chatMgr.GetMessages("fleet", 20)
	}
	if len(persisted) < 2 {
		t.Fatalf("expected persisted fleet chat messages, got %d", len(persisted))
	}
}

func TestHandleFleetChatPage_RendersTemplate(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-1", "web-01", "linux", "amd64")

	req := httptest.NewRequest(http.MethodGet, "/fleet/chat", nil)
	rr := httptest.NewRecorder()

	srv.handleFleetChatPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Fleet Chat") {
		t.Fatalf("expected fleet chat page content, got: %s", rr.Body.String())
	}
}

func TestHandleJobsPage_RendersTemplate(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rr := httptest.NewRecorder()

	srv.handleJobsPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Jobs") {
		t.Fatalf("expected jobs page content, got: %s", body)
	}
}

func TestHandleFederationPage_RendersTemplate(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/federation", nil)
	rr := httptest.NewRecorder()

	srv.handleFederationPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Federation") {
		t.Fatalf("expected federation page content, got: %s", body)
	}
}

func TestHandleApplyPolicy_NotFound(t *testing.T) {
	srv := newTestServer(t)

	missingProbeReq := httptest.NewRequest(http.MethodPost, "/api/v1/probes/missing/apply-policy/observe-only", nil)
	missingProbeReq.SetPathValue("id", "missing")
	missingProbeReq.SetPathValue("policyId", "observe-only")
	missingProbeRR := httptest.NewRecorder()

	srv.handleApplyPolicy(missingProbeRR, missingProbeReq)

	if missingProbeRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing probe, got %d body=%s", missingProbeRR.Code, missingProbeRR.Body.String())
	}

	srv.fleetMgr.Register("probe-a", "host", "linux", "amd64")
	missingPolicyReq := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-a/apply-policy/missing", nil)
	missingPolicyReq.SetPathValue("id", "probe-a")
	missingPolicyReq.SetPathValue("policyId", "missing")
	missingPolicyRR := httptest.NewRecorder()

	srv.handleApplyPolicy(missingPolicyRR, missingPolicyReq)

	if missingPolicyRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing policy, got %d body=%s", missingPolicyRR.Code, missingPolicyRR.Body.String())
	}
}

func TestHandleApplyPolicy_AppliedLocallyWhenProbeOffline(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-policy", "host", "linux", "amd64")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-policy/apply-policy/observe-only", nil)
	req.SetPathValue("id", "probe-policy")
	req.SetPathValue("policyId", "observe-only")
	rr := httptest.NewRecorder()

	srv.handleApplyPolicy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "applied_locally" {
		t.Fatalf("expected applied_locally status, got %#v", got)
	}

	ps, ok := srv.fleetMgr.Get("probe-policy")
	if !ok {
		t.Fatal("probe missing after apply")
	}
	if ps.PolicyLevel != protocol.CapObserve {
		t.Fatalf("expected policy level observe, got %s", ps.PolicyLevel)
	}
}
