package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

func TestHandleApprovalActionRequiresAPIBridge(t *testing.T) {
	t.Parallel()

	srv := &Server{config: Config{BasePath: ""}, log: logr.Discard()}
	req := httptest.NewRequest(http.MethodPost, "/approvals/req-1/approve", strings.NewReader("reason=test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &OIDCUser{Subject: "u1"}))

	rr := httptest.NewRecorder()
	srv.handleApprovalAction(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleApprovalActionForwardsToAPI(t *testing.T) {
	t.Parallel()

	var got struct {
		Authorization string
		Decision      string
		Reason        string
		Path          string
	}

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		got.Path = r.URL.Path
		got.Authorization = r.Header.Get("Authorization")
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		got.Decision = body["decision"]
		got.Reason = body["reason"]
		w.WriteHeader(http.StatusOK)
	}))
	defer apiSrv.Close()

	srv := &Server{
		config:     Config{BasePath: "", APIBaseURL: apiSrv.URL},
		log:        logr.Discard(),
		httpClient: apiSrv.Client(),
	}
	body := url.Values{"reason": []string{"typed-confirm"}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/approvals/req-1/approve", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &OIDCUser{
		Subject: "user-1", Email: "user@example.com", Name: "User", Groups: []string{"legator-operator"},
	}))

	rr := httptest.NewRecorder()
	srv.handleApprovalAction(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got.Path != "/api/v1/approvals/req-1" {
		t.Fatalf("path = %q, want /api/v1/approvals/req-1", got.Path)
	}
	if got.Decision != "approve" {
		t.Fatalf("decision = %q, want approve", got.Decision)
	}
	if got.Reason != "typed-confirm" {
		t.Fatalf("reason = %q, want typed-confirm", got.Reason)
	}
	if !strings.HasPrefix(got.Authorization, "Bearer ") {
		t.Fatalf("missing bearer auth: %q", got.Authorization)
	}
}

func TestHandleApprovalActionPropagatesForbidden(t *testing.T) {
	t.Parallel()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer apiSrv.Close()

	srv := &Server{
		config:     Config{BasePath: "", APIBaseURL: apiSrv.URL},
		log:        logr.Discard(),
		httpClient: apiSrv.Client(),
	}

	req := httptest.NewRequest(http.MethodPost, "/approvals/req-1/deny", strings.NewReader("reason=nope"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &OIDCUser{Subject: "u1", Email: "viewer@example.com"}))

	rr := httptest.NewRecorder()
	srv.handleApprovalAction(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if !strings.Contains(rr.Body.String(), "forbidden") {
		t.Fatalf("expected forbidden body, got %q", rr.Body.String())
	}
}

func TestMakeDashboardJWTIncludesUserClaims(t *testing.T) {
	t.Parallel()

	tok := makeDashboardJWT(&OIDCUser{Subject: "sub-1", Email: "u@example.com", Name: "User", Groups: []string{"legator-operator"}})
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		t.Fatalf("invalid token format: %q", tok)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if claims["email"] != "u@example.com" {
		t.Fatalf("email claim = %v", claims["email"])
	}
	if claims["sub"] != "sub-1" {
		t.Fatalf("sub claim = %v", claims["sub"])
	}
}
