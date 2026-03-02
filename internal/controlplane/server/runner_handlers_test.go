package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/runner"
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
