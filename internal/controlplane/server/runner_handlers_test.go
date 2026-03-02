package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestCreateRunnerRejectsLongLivedSecretFields(t *testing.T) {
	srv := newAuthTestServer(t)

	user, err := srv.userStore.Create("runner-op", "Runner Operator", "secret", "operator")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sess, err := srv.sessionStore.Create(user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	create := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runners", sess.ID,
		`{"label":"ephemeral-ci","aws_secret_access_key":"never"}`)
	if create.Code != http.StatusBadRequest {
		t.Fatalf("expected create runner 400 for secret field, got %d body=%s", create.Code, create.Body.String())
	}
	var payload APIError
	if err := json.Unmarshal(create.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "invalid_request" {
		t.Fatalf("expected invalid_request code, got %+v", payload)
	}
}

func TestIssueRunTokenRejectsTTLOverCap(t *testing.T) {
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

	issue := makeSessionRequest(t, srv, http.MethodPost, "/api/v1/runs", sess.ID,
		`{"runner_id":"`+created.ID+`","audience":"runner:start","ttl_seconds":600}`)
	if issue.Code != http.StatusBadRequest {
		t.Fatalf("expected ttl rejection 400, got %d body=%s", issue.Code, issue.Body.String())
	}
	var payload APIError
	if err := json.Unmarshal(issue.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode ttl error payload: %v", err)
	}
	if payload.Code != "invalid_request" {
		t.Fatalf("expected invalid_request code, got %+v", payload)
	}
}
