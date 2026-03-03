package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/runner"
	"github.com/marcus-qen/legator/internal/controlplane/tokenbroker"
)

func makeSessionRequest(t *testing.T, srv *Server, method, path, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody *bytes.Reader
	if body == "" {
		reqBody = bytes.NewReader(nil)
	} else {
		reqBody = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reqBody)
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionID})
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(w, req)
	return w
}

type fakeExecutionBackend struct {
	mu sync.Mutex

	startCalls    []runner.StartExecutionRequest
	stopCalls     []runner.StopExecutionRequest
	teardownCalls []runner.TeardownExecutionRequest

	startErr    error
	stopErr     error
	teardownErr error
}

func (f *fakeExecutionBackend) Start(_ context.Context, req runner.StartExecutionRequest) (*runner.StartExecutionResult, error) {
	f.mu.Lock()
	f.startCalls = append(f.startCalls, req)
	f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &runner.StartExecutionResult{ContainerID: "ctr-1", ContainerName: "legator-runner"}, nil
}

func (f *fakeExecutionBackend) Stop(_ context.Context, req runner.StopExecutionRequest) error {
	f.mu.Lock()
	f.stopCalls = append(f.stopCalls, req)
	f.mu.Unlock()
	return f.stopErr
}

func (f *fakeExecutionBackend) Teardown(_ context.Context, req runner.TeardownExecutionRequest) error {
	f.mu.Lock()
	f.teardownCalls = append(f.teardownCalls, req)
	f.mu.Unlock()
	return f.teardownErr
}

func TestIssueRunTokenRequiresSessionContext(t *testing.T) {
	srv := newAuthTestServer(t)
	token := createAPIKey(t, srv, "exec-only", auth.PermCommandExec)

	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/runs", token, `{"runner_id":"r1","audience":"runner:start"}`)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when session context missing, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload APIError
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "session_required" {
		t.Fatalf("expected session_required code, got %+v", payload)
	}
}

func TestRunnerLifecycleViaSessionBoundRunTokens(t *testing.T) {
	srv := newAuthTestServer(t)

	user, err := srv.userStore.Create("runner-op", "Runner Operator", "secret", "operator")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := srv.sessionStore.Create(user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	create := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners", sess.ID, `{"label":"ephemeral-ci"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("expected create runner 201, got %d body=%s", create.Code, create.Body.String())
	}
	var created runner.Runner
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected runner id in create response")
	}
	if created.State != runner.StateCreated {
		t.Fatalf("expected created state, got %s", created.State)
	}

	issue := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs", sess.ID,
		`{"runner_id":"`+created.ID+`","audience":"runner:start","ttl_seconds":30}`)
	if issue.Code != http.StatusCreated {
		t.Fatalf("expected issue token 201, got %d body=%s", issue.Code, issue.Body.String())
	}
	var issued runner.IssuedToken
	if err := json.Unmarshal(issue.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("expected non-empty run token")
	}
	if issued.Audience != runner.AudienceRunnerStart {
		t.Fatalf("expected runner:start audience, got %s", issued.Audience)
	}
	if len(issued.Scopes) != 1 || issued.Scopes[0] != string(runner.AudienceRunnerStart) {
		t.Fatalf("expected least-privilege scope runner:start, got %+v", issued.Scopes)
	}
	if issued.Issuer != "runner-op" {
		t.Fatalf("expected issuer runner-op, got %s", issued.Issuer)
	}
	if issued.ExpiresAt.Sub(issued.IssuedAt) > 31*time.Second {
		t.Fatalf("expected short-lived token, got ttl=%s", issued.ExpiresAt.Sub(issued.IssuedAt))
	}

	start := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners/"+created.ID+"/start", sess.ID,
		`{"run_token":"`+issued.Token+`"}`)
	if start.Code != http.StatusOK {
		t.Fatalf("expected start 200, got %d body=%s", start.Code, start.Body.String())
	}
	var started runner.Runner
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if started.State != runner.StateRunning {
		t.Fatalf("expected running state, got %s", started.State)
	}

	reuse := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners/"+created.ID+"/start", sess.ID,
		`{"run_token":"`+issued.Token+`"}`)
	if reuse.Code != http.StatusConflict {
		t.Fatalf("expected consumed-token conflict, got %d body=%s", reuse.Code, reuse.Body.String())
	}
	var consumedErr APIError
	if err := json.Unmarshal(reuse.Body.Bytes(), &consumedErr); err != nil {
		t.Fatalf("decode consume error payload: %v", err)
	}
	if consumedErr.Code != "run_token_consumed" {
		t.Fatalf("expected run_token_consumed code, got %+v", consumedErr)
	}

	events := srv.queryAudit(audit.Filter{Limit: 50})
	seenCreate := false
	seenIssue := false
	seenStart := false
	for _, evt := range events {
		switch evt.Type {
		case audit.EventRunnerCreated:
			seenCreate = true
		case audit.EventRunnerRunTokenIssued:
			seenIssue = true
		case audit.EventRunnerStarted:
			seenStart = true
		}
	}
	if !seenCreate || !seenIssue || !seenStart {
		t.Fatalf("expected runner audit markers create=%v issue=%v start=%v", seenCreate, seenIssue, seenStart)
	}
}

func TestRunnerLifecycleSandboxBackendStartStopTeardown(t *testing.T) {
	srv := newAuthTestServer(t)
	backend := &fakeExecutionBackend{}
	srv.runnerExecutionBackend = backend

	user, err := srv.userStore.Create("runner-op", "Runner Operator", "secret", "operator")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := srv.sessionStore.Create(user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	create := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners", sess.ID,
		`{"label":"sandbox-ci","job_id":"job-42","backend":"sandbox","sandbox":{"image":"alpine:3.20","command":["sh","-lc","sleep 30"],"timeout_seconds":60}}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("expected sandbox create 201, got %d body=%s", create.Code, create.Body.String())
	}
	var created runner.Runner
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Backend != runner.BackendSandbox {
		t.Fatalf("expected sandbox backend, got %s", created.Backend)
	}

	issueStart := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs", sess.ID,
		`{"runner_id":"`+created.ID+`","job_id":"job-42","audience":"runner:start","ttl_seconds":60}`)
	if issueStart.Code != http.StatusCreated {
		t.Fatalf("issue start token: status=%d body=%s", issueStart.Code, issueStart.Body.String())
	}
	var startToken runner.IssuedToken
	if err := json.Unmarshal(issueStart.Body.Bytes(), &startToken); err != nil {
		t.Fatalf("decode start token: %v", err)
	}

	start := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners/"+created.ID+"/start", sess.ID,
		`{"run_token":"`+startToken.Token+`","job_id":"job-42"}`)
	if start.Code != http.StatusOK {
		t.Fatalf("start runner: status=%d body=%s", start.Code, start.Body.String())
	}

	issueStop := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs", sess.ID,
		`{"runner_id":"`+created.ID+`","job_id":"job-42","audience":"runner:stop","ttl_seconds":60}`)
	if issueStop.Code != http.StatusCreated {
		t.Fatalf("issue stop token: status=%d body=%s", issueStop.Code, issueStop.Body.String())
	}
	var stopToken runner.IssuedToken
	if err := json.Unmarshal(issueStop.Body.Bytes(), &stopToken); err != nil {
		t.Fatalf("decode stop token: %v", err)
	}

	stop := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners/"+created.ID+"/stop", sess.ID,
		`{"run_token":"`+stopToken.Token+`","job_id":"job-42"}`)
	if stop.Code != http.StatusOK {
		t.Fatalf("stop runner: status=%d body=%s", stop.Code, stop.Body.String())
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.startCalls) != 1 {
		t.Fatalf("expected 1 backend start call, got %d", len(backend.startCalls))
	}
	if backend.startCalls[0].RunnerID != created.ID || backend.startCalls[0].JobID != "job-42" {
		t.Fatalf("unexpected start request: %+v", backend.startCalls[0])
	}
	if len(backend.stopCalls) != 1 {
		t.Fatalf("expected 1 backend stop call, got %d", len(backend.stopCalls))
	}
	if len(backend.teardownCalls) != 1 {
		t.Fatalf("expected 1 backend teardown call, got %d", len(backend.teardownCalls))
	}
}

func TestRunnerLifecycleJobScopeRejected(t *testing.T) {
	srv := newAuthTestServer(t)
	backend := &fakeExecutionBackend{}
	srv.runnerExecutionBackend = backend

	user, err := srv.userStore.Create("runner-op", "Runner Operator", "secret", "operator")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := srv.sessionStore.Create(user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	create := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners", sess.ID,
		`{"label":"sandbox-ci","job_id":"job-expected","backend":"sandbox","sandbox":{"command":["echo","ok"]}}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d body=%s", create.Code, create.Body.String())
	}
	var created runner.Runner
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	issue := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs", sess.ID,
		`{"runner_id":"`+created.ID+`","job_id":"job-expected","audience":"runner:start"}`)
	if issue.Code != http.StatusCreated {
		t.Fatalf("issue token: status=%d body=%s", issue.Code, issue.Body.String())
	}
	var token runner.IssuedToken
	if err := json.Unmarshal(issue.Body.Bytes(), &token); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	start := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners/"+created.ID+"/start", sess.ID,
		`{"run_token":"`+token.Token+`","job_id":"job-other"}`)
	if start.Code != http.StatusForbidden {
		t.Fatalf("expected scope rejection 403, got %d body=%s", start.Code, start.Body.String())
	}
	var payload APIError
	if err := json.Unmarshal(start.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode scope error: %v", err)
	}
	if payload.Code != "run_token_scope_rejected" {
		t.Fatalf("expected run_token_scope_rejected, got %+v", payload)
	}
}

func makeAbsolutePathRequest(t *testing.T, srv *Server, method, absoluteURL, body string) *httptest.ResponseRecorder {
	t.Helper()

	u, err := url.Parse(absoluteURL)
	if err != nil {
		t.Fatalf("parse absolute url: %v", err)
	}

	var reqBody *bytes.Reader
	if body == "" {
		reqBody = bytes.NewReader(nil)
	} else {
		reqBody = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, u.RequestURI(), reqBody)
	if body != "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)
	return rr
}

func TestRunnerArtifactPresignedUploadAndDownload(t *testing.T) {
	srv := newAuthTestServer(t)

	user, err := srv.userStore.Create("runner-op", "Runner Operator", "secret", "operator")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := srv.sessionStore.Create(user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	presignUpload := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs/run-42/artifacts/presign", sess.ID,
		`{"path":"workspace/run-42/stdout.log","scope":"workspace/run-42","operation":"upload","ttl_seconds":60}`)
	if presignUpload.Code != http.StatusCreated {
		t.Fatalf("presign upload: status=%d body=%s", presignUpload.Code, presignUpload.Body.String())
	}
	var uploadResp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(presignUpload.Body.Bytes(), &uploadResp); err != nil {
		t.Fatalf("decode upload presign: %v", err)
	}
	if uploadResp.URL == "" {
		t.Fatalf("expected upload url")
	}

	upload := makeAbsolutePathRequest(t, srv, http.MethodPut, uploadResp.URL, "hello-from-runner")
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload artifact: status=%d body=%s", upload.Code, upload.Body.String())
	}

	presignDownload := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs/run-42/artifacts/presign", sess.ID,
		`{"path":"workspace/run-42/stdout.log","scope":"workspace/run-42","operation":"download","ttl_seconds":60}`)
	if presignDownload.Code != http.StatusCreated {
		t.Fatalf("presign download: status=%d body=%s", presignDownload.Code, presignDownload.Body.String())
	}
	var downloadResp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(presignDownload.Body.Bytes(), &downloadResp); err != nil {
		t.Fatalf("decode download presign: %v", err)
	}
	if downloadResp.URL == "" {
		t.Fatalf("expected download url")
	}

	download := makeAbsolutePathRequest(t, srv, http.MethodGet, downloadResp.URL, "")
	if download.Code != http.StatusOK {
		t.Fatalf("download artifact: status=%d body=%s", download.Code, download.Body.String())
	}
	if got := download.Body.String(); got != "hello-from-runner" {
		t.Fatalf("unexpected artifact body: got %q", got)
	}
}

func TestRunnerArtifactPresignedOutOfScopeRejectedWithAudit(t *testing.T) {
	srv := newAuthTestServer(t)

	user, err := srv.userStore.Create("runner-op", "Runner Operator", "secret", "operator")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := srv.sessionStore.Create(user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	presignUpload := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs/run-7/artifacts/presign", sess.ID,
		`{"path":"workspace/run-7/out/artifact.txt","scope":"workspace/run-7","operation":"upload","ttl_seconds":60}`)
	if presignUpload.Code != http.StatusCreated {
		t.Fatalf("presign upload: status=%d body=%s", presignUpload.Code, presignUpload.Body.String())
	}
	var uploadResp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(presignUpload.Body.Bytes(), &uploadResp); err != nil {
		t.Fatalf("decode upload presign: %v", err)
	}

	u, err := url.Parse(uploadResp.URL)
	if err != nil {
		t.Fatalf("parse upload url: %v", err)
	}
	token := u.Query().Get("token")
	if token == "" {
		t.Fatalf("expected token in presigned url")
	}

	tamperedURL := "http://example.com/artifacts/runs/run-7/workspace/run-8/out/escape.txt?token=" + url.QueryEscape(token)
	rejected := makeAbsolutePathRequest(t, srv, http.MethodPut, tamperedURL, "bad-write")
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("expected scope rejection 403, got %d body=%s", rejected.Code, rejected.Body.String())
	}
	var payload APIError
	if err := json.Unmarshal(rejected.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "artifact_scope_rejected" {
		t.Fatalf("expected artifact_scope_rejected, got %+v", payload)
	}

	events := srv.queryAudit(audit.Filter{Type: audit.EventRunnerArtifactAccessDenied, Limit: 10})
	if len(events) == 0 {
		t.Fatalf("expected artifact access denied audit event")
	}
	foundScopeReason := false
	for _, evt := range events {
		detail, ok := evt.Detail.(map[string]any)
		if !ok {
			continue
		}
		errVal, _ := detail["error"].(string)
		if strings.Contains(errVal, "scope") {
			foundScopeReason = true
			break
		}
	}
	if !foundScopeReason {
		t.Fatalf("expected scope rejection reason in audit detail, events=%+v", events)
	}
}

func issueProviderProxyToken(t *testing.T, broker *tokenbroker.Broker, runID, runnerID, sessionID string, ttl time.Duration) string {
	t.Helper()

	issued, err := broker.Issue(tokenbroker.IssueRequest{
		RunID:     runID,
		ProbeID:   runnerID,
		Audience:  string(runner.AudienceRunnerProviderProxy),
		Scopes:    []string{string(runner.AudienceRunnerProviderProxy)},
		Issuer:    "runner-agent",
		SessionID: sessionID,
		TTL:       ttl,
	})
	if err != nil {
		t.Fatalf("issue provider proxy token: %v", err)
	}
	return issued.Token
}

func TestRunProviderProxyValidTokenSuccessAndAudit(t *testing.T) {
	var upstreamAuthHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuthHeader = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-proxy","choices":[{"message":{"content":"proxy-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer upstream.Close()

	t.Setenv("LEGATOR_LLM_PROVIDER", "openai")
	t.Setenv("LEGATOR_LLM_BASE_URL", upstream.URL)
	t.Setenv("LEGATOR_LLM_API_KEY", "super-secret-provider-key")
	t.Setenv("LEGATOR_LLM_MODEL", "gpt-proxy")

	srv := newAuthTestServer(t)
	token := issueProviderProxyToken(t, srv.runTokenBroker, "run-123", "runner-9", "sess-runner", 2*time.Minute)

	body := `{"run_token":"` + token + `","session_id":"sess-runner","messages":[{"role":"user","content":"hello"}]}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/runs/run-123/provider-proxy", "", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if upstreamAuthHeader != "Bearer super-secret-provider-key" {
		t.Fatalf("expected upstream bearer auth to be injected, got %q", upstreamAuthHeader)
	}
	if strings.Contains(rr.Body.String(), "super-secret-provider-key") {
		t.Fatalf("response leaked provider key: %s", rr.Body.String())
	}

	var resp struct {
		RunID        string `json:"run_id"`
		Content      string `json:"content"`
		Model        string `json:"model"`
		FinishReason string `json:"finish_reason"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode proxy response: %v", err)
	}
	if resp.RunID != "run-123" || resp.Content != "proxy-ok" || resp.Model != "gpt-proxy" {
		t.Fatalf("unexpected proxy response: %+v", resp)
	}

	events := srv.queryAudit(audit.Filter{Limit: 20})
	found := false
	for _, evt := range events {
		if !strings.Contains(evt.Summary, "Runner provider proxy completed: run-123") {
			continue
		}
		detail, ok := evt.Detail.(map[string]any)
		if !ok {
			continue
		}
		if detail["run_id"] == "run-123" && detail["allow"] == true {
			found = true
			if detail["provider_status"] != float64(http.StatusOK) && detail["provider_status"] != http.StatusOK {
				t.Fatalf("expected provider_status=200 in audit detail, got %+v", detail)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected provider proxy success audit event, got events=%+v", events)
	}
}

func TestRunProviderProxyExpiredTokenReturnsUnauthorized(t *testing.T) {
	srv := newAuthTestServer(t)

	now := time.Date(2026, 3, 3, 1, 0, 0, 0, time.UTC)
	broker := tokenbroker.NewBroker(tokenbroker.Config{Now: func() time.Time { return now }})
	t.Cleanup(func() { _ = broker.Close() })
	srv.runTokenBroker = broker

	token := issueProviderProxyToken(t, broker, "run-expired", "runner-9", "sess-expired", time.Second)
	now = now.Add(2 * time.Second)

	body := `{"run_token":"` + token + `","session_id":"sess-expired","messages":[{"role":"user","content":"hello"}]}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/runs/run-expired/provider-proxy", "", body)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload APIError
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "expired_run_token" {
		t.Fatalf("expected expired_run_token, got %+v", payload)
	}

	events := srv.queryAudit(audit.Filter{Type: audit.EventAuthorizationDenied, Limit: 10})
	found := false
	for _, evt := range events {
		detail, ok := evt.Detail.(map[string]any)
		if !ok {
			continue
		}
		if detail["run_id"] == "run-expired" && detail["allow"] == false {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected auth denied audit event for expired token, events=%+v", events)
	}
}

func TestRunProviderProxySessionMismatchReturnsForbidden(t *testing.T) {
	srv := newAuthTestServer(t)
	token := issueProviderProxyToken(t, srv.runTokenBroker, "run-bound", "runner-9", "sess-good", 2*time.Minute)

	body := `{"run_token":"` + token + `","session_id":"sess-bad","messages":[{"role":"user","content":"hello"}]}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/runs/run-bound/provider-proxy", "", body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload APIError
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "run_token_scope_rejected" {
		t.Fatalf("expected run_token_scope_rejected, got %+v", payload)
	}
}
