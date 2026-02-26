package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

type stubUserAuthenticator struct {
	user     *UserInfo
	err      error
	username string
	password string
}

func (s *stubUserAuthenticator) Authenticate(username, password string) (*UserInfo, error) {
	s.username = username
	s.password = password
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

type stubSessionCreator struct {
	token  string
	err    error
	userID string
}

func (s *stubSessionCreator) Create(userID string) (string, error) {
	s.userID = userID
	if s.err != nil {
		return "", s.err
	}
	return s.token, nil
}

type stubSessionDeleter struct {
	deletedToken string
	err          error
}

func (s *stubSessionDeleter) Delete(token string) error {
	s.deletedToken = token
	return s.err
}

func TestHandleLoginPageRenders(t *testing.T) {
	h := HandleLoginPage(filepath.Join("..", "..", "..", "web", "templates"))
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Legator") {
		t.Fatalf("expected Legator branding in body, got: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "action=\"/login\"") {
		t.Fatalf("expected login form action in body")
	}
}

func TestHandleLoginValidSetsCookieAndRedirects(t *testing.T) {
	userAuth := &stubUserAuthenticator{user: &UserInfo{ID: "user-1", Username: "alice", Role: "admin"}}
	creator := &stubSessionCreator{token: "sess_abc123"}

	h := HandleLogin(userAuth, creator)
	form := url.Values{}
	form.Set("username", "alice")
	form.Set("password", "correct-horse")

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected redirect to /, got %q", loc)
	}
	if creator.userID != "user-1" {
		t.Fatalf("expected session to be created for user-1, got %q", creator.userID)
	}

	resp := w.Result()
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie to be set")
	}
	if sessionCookie.Value != "sess_abc123" {
		t.Fatalf("unexpected cookie value %q", sessionCookie.Value)
	}
	if !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatalf("session cookie should be HttpOnly + Secure")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("expected SameSite=Lax, got %v", sessionCookie.SameSite)
	}
	if sessionCookie.Path != "/" {
		t.Fatalf("expected cookie Path=/, got %q", sessionCookie.Path)
	}
	if sessionCookie.MaxAge != sessionMaxAgeSeconds {
		t.Fatalf("expected MaxAge=%d, got %d", sessionMaxAgeSeconds, sessionCookie.MaxAge)
	}
}

func TestHandleLoginInvalidShowsError(t *testing.T) {
	userAuth := &stubUserAuthenticator{err: errors.New("invalid credentials")}
	creator := &stubSessionCreator{}
	h := HandleLogin(userAuth, creator)

	form := url.Values{}
	form.Set("username", "alice")
	form.Set("password", "wrong")

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid credentials") {
		t.Fatalf("expected invalid credentials error, got %s", w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			t.Fatalf("unexpected non-empty session cookie on failure")
		}
	}
}

func TestHandleLogoutClearsCookieAndRedirects(t *testing.T) {
	deleter := &stubSessionDeleter{}
	h := HandleLogout(deleter)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess_to_delete"})
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
	if deleter.deletedToken != "sess_to_delete" {
		t.Fatalf("expected delete called with session token, got %q", deleter.deletedToken)
	}

	resp := w.Result()
	var cleared *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			cleared = c
			break
		}
	}
	if cleared == nil {
		t.Fatalf("expected cleared session cookie")
	}
	if cleared.Value != "" {
		t.Fatalf("expected empty cookie value, got %q", cleared.Value)
	}
	if cleared.MaxAge >= 0 {
		t.Fatalf("expected MaxAge < 0 for cleared cookie, got %d", cleared.MaxAge)
	}
}

func TestHandleMeReturnsUserFromContext(t *testing.T) {
	h := HandleMe()
	user := &AuthenticatedUser{ID: "user-1", Username: "alice", Role: "admin", Permissions: []Permission{PermAdmin}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, user))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var got AuthenticatedUser
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Username != "alice" || got.Role != "admin" {
		t.Fatalf("unexpected /me payload: %#v", got)
	}
}
