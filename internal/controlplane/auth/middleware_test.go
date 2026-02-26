package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type stubSessionValidator struct {
	session *SessionInfo
	err     error
	token   string
}

func (s *stubSessionValidator) Validate(token string) (*SessionInfo, error) {
	s.token = token
	if s.err != nil {
		return nil, s.err
	}
	return s.session, nil
}

type stubPermissionResolver struct {
	perms []Permission
	role  string
}

func (s *stubPermissionResolver) PermissionsForRole(role string) []Permission {
	s.role = role
	return s.perms
}

func TestMiddlewareSessionCookieAuthPathWorks(t *testing.T) {
	validator := &stubSessionValidator{session: &SessionInfo{
		Token:    "sess-valid",
		UserID:   "user-1",
		Username: "alice",
		Role:     "operator",
	}}
	resolver := &stubPermissionResolver{perms: []Permission{PermFleetRead, PermAuditRead}}

	mw := NewMiddleware(nil, nil)
	mw.SetSessionAuth(validator, resolver)

	var gotUser *AuthenticatedUser
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess-valid"})
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if gotUser == nil {
		t.Fatal("expected session user in context")
	}
	if gotUser.Username != "alice" || gotUser.Role != "operator" {
		t.Fatalf("unexpected user context: %#v", gotUser)
	}
	if validator.token != "sess-valid" {
		t.Fatalf("expected validator called with session cookie, got %q", validator.token)
	}
	if resolver.role != "operator" {
		t.Fatalf("expected resolver called with role operator, got %q", resolver.role)
	}
}

func TestMiddlewareAPIKeyStillWorks(t *testing.T) {
	ks, err := NewKeyStore(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	_, plain, err := ks.Create("ci-token", []Permission{PermAdmin}, nil)
	if err != nil {
		t.Fatal(err)
	}

	mw := NewMiddleware(ks, nil)
	var got *APIKey
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got == nil || got.Name != "ci-token" {
		t.Fatalf("expected api key in context, got %#v", got)
	}
}

func TestMiddlewareRedirectsWebPageToLoginWhenUnauthenticated(t *testing.T) {
	mw := NewMiddleware(nil, nil)
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/login" {
		t.Fatalf("expected redirect to /login, got %q", got)
	}
}
