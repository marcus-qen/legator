package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/users"
	"go.uber.org/zap"
)

type stubSessionCreator struct {
	token  string
	userID string
	err    error
}

func (s *stubSessionCreator) Create(userID string) (string, error) {
	s.userID = userID
	if s.err != nil {
		return "", s.err
	}
	return s.token, nil
}

type mockOIDCProvider struct {
	server *httptest.Server
	issuer string
	kid    string
	key    *rsa.PrivateKey

	mu         sync.Mutex
	nextNonce  string
	nextClaims map[string]any
}

func newMockOIDCProvider(t *testing.T, clientID string) *mockOIDCProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	mock := &mockOIDCProvider{key: key, kid: "test-key"}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 mock.issuer,
			"authorization_endpoint": mock.issuer + "/authorize",
			"token_endpoint":         mock.issuer + "/token",
			"jwks_uri":               mock.issuer + "/keys",
		})
	})

	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		pub := &mock.key.PublicKey
		n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"kid": mock.kid,
				"alg": "RS256",
				"use": "sig",
				"n":   n,
				"e":   e,
			}},
		})
	})

	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(r.FormValue("code")) == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		idToken, err := mock.signIDToken(clientID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = fmt.Fprintf(w, "access_token=%s&token_type=Bearer&expires_in=300&id_token=%s", url.QueryEscape("access-token"), url.QueryEscape(idToken))
	})

	mock.server = httptest.NewServer(mux)
	mock.issuer = mock.server.URL

	return mock
}

func (m *mockOIDCProvider) Close() {
	m.server.Close()
}

func (m *mockOIDCProvider) setClaims(nonce string, claims map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextNonce = nonce
	m.nextClaims = claims
}

func (m *mockOIDCProvider) signIDToken(clientID string) (string, error) {
	m.mu.Lock()
	nonce := m.nextNonce
	claims := m.nextClaims
	m.mu.Unlock()

	if claims == nil {
		claims = map[string]any{}
	}

	now := time.Now().Unix()
	payload := map[string]any{
		"iss":   m.issuer,
		"aud":   clientID,
		"iat":   now,
		"exp":   now + 300,
		"nonce": nonce,
		"sub":   "user-123",
	}
	for k, v := range claims {
		payload[k] = v
	}

	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": m.kid}
	return signJWT(header, payload, m.key)
}

func signJWT(header, payload map[string]any, key *rsa.PrivateKey) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	hEnc := base64.RawURLEncoding.EncodeToString(h)
	pEnc := base64.RawURLEncoding.EncodeToString(p)
	signingInput := hEnc + "." + pEnc
	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func newTestUserStore(t *testing.T) *users.Store {
	t.Helper()
	store, err := users.NewStore(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("create user store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestProvider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	p, err := NewProvider(t.Context(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	return p
}

func extractCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestHandleLoginAndCallbackValidFlowCreatesSession(t *testing.T) {
	const clientID = "legator-test"
	mock := newMockOIDCProvider(t, clientID)
	defer mock.Close()

	cfg := Config{
		Enabled:         true,
		ProviderURL:     mock.issuer,
		ClientID:        clientID,
		ClientSecret:    "secret",
		RedirectURL:     "https://legator.example.com/auth/oidc/callback",
		Scopes:          []string{"openid", "profile", "email", "groups"},
		RoleClaim:       "groups",
		RoleMapping:     map[string]string{"developers": "operator"},
		DefaultRole:     "viewer",
		AutoCreateUsers: true,
		ProviderName:    "Keycloak",
	}
	provider := newTestProvider(t, cfg)
	usersStore := newTestUserStore(t)
	sessions := &stubSessionCreator{token: "sess-token"}

	loginReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil)
	loginRec := httptest.NewRecorder()
	provider.HandleLogin(loginRec, loginReq)

	if loginRec.Code != http.StatusFound {
		t.Fatalf("expected login redirect, got %d", loginRec.Code)
	}
	redirectURL := loginRec.Header().Get("Location")
	parsedRedirect, err := url.Parse(redirectURL)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	q := parsedRedirect.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("expected PKCE challenge method S256, got %q", q.Get("code_challenge_method"))
	}
	if strings.TrimSpace(q.Get("code_challenge")) == "" {
		t.Fatal("missing PKCE code_challenge")
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("missing state")
	}

	stateCookie := extractCookie(loginRec.Result(), stateCookieName)
	if stateCookie == nil {
		t.Fatal("missing oidc state cookie")
	}
	stored, err := decodeState(stateCookie.Value)
	if err != nil {
		t.Fatalf("decode state cookie: %v", err)
	}

	mock.setClaims(stored.Nonce, map[string]any{
		"sub":                "alice-subject",
		"preferred_username": "alice",
		"email":              "alice@example.com",
		"name":               "Alice",
		"groups":             []string{"developers"},
	})

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=test-code&state="+url.QueryEscape(state), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	provider.HandleCallback(usersStore, sessions).ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("expected callback redirect, got %d body=%s", cbRec.Code, cbRec.Body.String())
	}
	if loc := cbRec.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected redirect to /, got %q", loc)
	}
	if sessions.userID != "oidc:alice-subject" {
		t.Fatalf("expected session user id oidc:alice-subject, got %q", sessions.userID)
	}
	sessCookie := extractCookie(cbRec.Result(), auth.SessionCookieName)
	if sessCookie == nil || sessCookie.Value != "sess-token" {
		t.Fatalf("expected session cookie to be set, got %#v", sessCookie)
	}

	created, err := usersStore.Get("oidc:alice-subject")
	if err != nil {
		t.Fatalf("expected oidc user created: %v", err)
	}
	if created.Username != "alice" || created.DisplayName != "Alice" {
		t.Fatalf("unexpected created user: %+v", created)
	}
}

func TestHandleCallbackRejectsInvalidState(t *testing.T) {
	const clientID = "legator-test"
	mock := newMockOIDCProvider(t, clientID)
	defer mock.Close()

	provider := newTestProvider(t, Config{
		Enabled:         true,
		ProviderURL:     mock.issuer,
		ClientID:        clientID,
		ClientSecret:    "secret",
		RedirectURL:     "https://legator.example.com/auth/oidc/callback",
		AutoCreateUsers: true,
	})
	usersStore := newTestUserStore(t)
	sessions := &stubSessionCreator{token: "sess-token"}

	loginReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil)
	loginRec := httptest.NewRecorder()
	provider.HandleLogin(loginRec, loginReq)
	stateCookie := extractCookie(loginRec.Result(), stateCookieName)
	if stateCookie == nil {
		t.Fatal("missing state cookie")
	}

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=test-code&state=wrong-state", nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	provider.HandleCallback(usersStore, sessions).ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid state, got %d", cbRec.Code)
	}
	if sessions.userID != "" {
		t.Fatalf("session should not be created on invalid state, got user %q", sessions.userID)
	}
}

func TestHandleCallbackAutoCreatesUnknownUser(t *testing.T) {
	const clientID = "legator-test"
	mock := newMockOIDCProvider(t, clientID)
	defer mock.Close()

	provider := newTestProvider(t, Config{
		Enabled:         true,
		ProviderURL:     mock.issuer,
		ClientID:        clientID,
		ClientSecret:    "secret",
		RedirectURL:     "https://legator.example.com/auth/oidc/callback",
		AutoCreateUsers: true,
		DefaultRole:     "viewer",
	})
	usersStore := newTestUserStore(t)
	sessions := &stubSessionCreator{token: "sess-token"}

	loginReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil)
	loginRec := httptest.NewRecorder()
	provider.HandleLogin(loginRec, loginReq)
	stateCookie := extractCookie(loginRec.Result(), stateCookieName)
	if stateCookie == nil {
		t.Fatal("missing state cookie")
	}
	stored, err := decodeState(stateCookie.Value)
	if err != nil {
		t.Fatalf("decode state cookie: %v", err)
	}

	mock.setClaims(stored.Nonce, map[string]any{
		"sub":                "new-user",
		"preferred_username": "newbie",
		"name":               "New User",
	})

	cbReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=ok&state="+url.QueryEscape(stored.State), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	provider.HandleCallback(usersStore, sessions).ServeHTTP(cbRec, cbReq)

	if cbRec.Code != http.StatusFound {
		t.Fatalf("expected successful callback, got %d body=%s", cbRec.Code, cbRec.Body.String())
	}
	created, err := usersStore.Get("oidc:new-user")
	if err != nil {
		t.Fatalf("expected user created: %v", err)
	}
	if created.Username != "newbie" {
		t.Fatalf("unexpected username %q", created.Username)
	}
}

func TestHandleCallbackMapsRoleFromGroupsClaim(t *testing.T) {
	const clientID = "legator-test"
	mock := newMockOIDCProvider(t, clientID)
	defer mock.Close()

	provider := newTestProvider(t, Config{
		Enabled:      true,
		ProviderURL:  mock.issuer,
		ClientID:     clientID,
		ClientSecret: "secret",
		RedirectURL:  "https://legator.example.com/auth/oidc/callback",
		RoleClaim:    "groups",
		RoleMapping: map[string]string{
			"viewers":         "viewer",
			"developers":      "operator",
			"platform-admins": "admin",
		},
		DefaultRole:     "viewer",
		AutoCreateUsers: true,
	})
	usersStore := newTestUserStore(t)
	sessions := &stubSessionCreator{token: "sess-token"}

	loginReq := httptest.NewRequest(http.MethodGet, "/auth/oidc/login", nil)
	loginRec := httptest.NewRecorder()
	provider.HandleLogin(loginRec, loginReq)
	stateCookie := extractCookie(loginRec.Result(), stateCookieName)
	if stateCookie == nil {
		t.Fatal("missing state cookie")
	}
	stored, err := decodeState(stateCookie.Value)
	if err != nil {
		t.Fatalf("decode state cookie: %v", err)
	}

	mock.setClaims(stored.Nonce, map[string]any{
		"sub":                "role-user",
		"preferred_username": "roley",
		"groups":             []string{"viewers", "platform-admins"},
	})

	cbReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/auth/oidc/callback?code=ok&state=%s", url.QueryEscape(stored.State)), nil)
	cbReq.AddCookie(stateCookie)
	cbRec := httptest.NewRecorder()
	provider.HandleCallback(usersStore, sessions).ServeHTTP(cbRec, cbReq)
	if cbRec.Code != http.StatusFound {
		t.Fatalf("expected successful callback, got %d body=%s", cbRec.Code, cbRec.Body.String())
	}

	created, err := usersStore.Get("oidc:role-user")
	if err != nil {
		t.Fatalf("fetch created user: %v", err)
	}
	if created.Role != "admin" {
		t.Fatalf("expected highest mapped role admin, got %q", created.Role)
	}
}
