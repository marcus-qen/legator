package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

type auditRecorderStub struct{}

func (auditRecorderStub) Record(_ audit.Event) {}

func (auditRecorderStub) Emit(_ audit.EventType, _, _, _ string) {}

type captureAuditRecorder struct {
	events []audit.Event
}

func (c *captureAuditRecorder) Record(evt audit.Event) {
	c.events = append(c.events, evt)
}

func (c *captureAuditRecorder) Emit(_ audit.EventType, _, _, _ string) {}

func newTestTokenStore(t *testing.T) *TokenStore {
	t.Helper()
	ts, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatalf("new token store: %v", err)
	}
	t.Cleanup(func() { _ = ts.Close() })
	return ts
}

func TestTokenGeneration(t *testing.T) {
	ts := newTestTokenStore(t)

	token := ts.Generate()
	if token.Value == "" {
		t.Fatal("empty token")
	}
	if token.Used {
		t.Error("new token should not be used")
	}
	if !token.Expires.After(token.Created) {
		t.Error("expiry should be after creation")
	}
}

func TestTokenConsume(t *testing.T) {
	ts := newTestTokenStore(t)
	token := ts.Generate()

	// First consume should succeed
	if !ts.Consume(token.Value) {
		t.Error("first consume should succeed")
	}

	// Second consume should fail (single-use)
	if ts.Consume(token.Value) {
		t.Error("second consume should fail")
	}
}

func TestTokenConsumeMultiUse(t *testing.T) {
	ts := newTestTokenStore(t)
	token := ts.GenerateWithOptions(GenerateOptions{MultiUse: true})

	if !ts.Consume(token.Value) {
		t.Fatal("first consume should succeed for multi-use token")
	}
	if !ts.Consume(token.Value) {
		t.Fatal("second consume should succeed for multi-use token")
	}
	if token.Used {
		t.Fatal("multi-use token should not be marked used")
	}
}

func TestTokenInvalid(t *testing.T) {
	ts := newTestTokenStore(t)

	if ts.Consume("nonexistent") {
		t.Error("nonexistent token should fail")
	}
}

func TestRegisterHandler(t *testing.T) {
	ts := newTestTokenStore(t)
	fm := fleet.NewManager(testLogger())
	handler := HandleRegister(ts, fm, testLogger())

	token := ts.Generate()

	reqBody := RegisterRequest{
		Token:    token.Value,
		Hostname: "test-host",
		OS:       "linux",
		Arch:     "amd64",
		Version:  "dev",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp RegisterResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.ProbeID == "" {
		t.Error("empty probe ID")
	}
	if resp.APIKey == "" {
		t.Error("empty API key")
	}

	// Verify probe is in fleet
	ps, ok := fm.Get(resp.ProbeID)
	if !ok {
		t.Fatal("probe not registered in fleet")
	}
	if ps.Hostname != "test-host" {
		t.Errorf("expected hostname test-host, got %s", ps.Hostname)
	}
}

func TestRegisterHandler_DeduplicatesByHostname(t *testing.T) {
	ts := newTestTokenStore(t)
	fm := fleet.NewManager(testLogger())
	handler := HandleRegister(ts, fm, testLogger())

	register := func(reqBody RegisterRequest) RegisterResponse {
		t.Helper()
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/v1/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp RegisterResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp
	}

	resp1 := register(RegisterRequest{
		Token:    ts.Generate().Value,
		Hostname: "dedup-host",
		OS:       "linux",
		Arch:     "amd64",
		Tags:     []string{"prod"},
	})

	resp2 := register(RegisterRequest{
		Token:    ts.Generate().Value,
		Hostname: "dedup-host",
		OS:       "linux",
		Arch:     "arm64",
		Tags:     []string{"canary"},
	})

	if resp1.ProbeID != resp2.ProbeID {
		t.Fatalf("expected same probe id for same hostname, got %s and %s", resp1.ProbeID, resp2.ProbeID)
	}
	if resp1.APIKey == resp2.APIKey {
		t.Fatal("expected API key rotation on re-registration")
	}

	ps, ok := fm.Get(resp1.ProbeID)
	if !ok {
		t.Fatal("expected probe in fleet")
	}
	if ps.APIKey != resp2.APIKey {
		t.Fatalf("expected latest api key %q, got %q", resp2.APIKey, ps.APIKey)
	}
	if ps.Arch != "arm64" {
		t.Fatalf("expected arch to refresh to arm64, got %s", ps.Arch)
	}
	if len(ps.Tags) != 1 || ps.Tags[0] != "canary" {
		t.Fatalf("expected tags to refresh to canary, got %#v", ps.Tags)
	}
	if len(fm.List()) != 1 {
		t.Fatalf("expected single fleet entry after re-registration, got %d", len(fm.List()))
	}
}

func TestRegisterHandler_DifferentHostnameGetsDifferentProbeID(t *testing.T) {
	ts := newTestTokenStore(t)
	fm := fleet.NewManager(testLogger())
	handler := HandleRegister(ts, fm, testLogger())

	register := func(reqBody RegisterRequest) RegisterResponse {
		t.Helper()
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/v1/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp RegisterResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp
	}

	resp1 := register(RegisterRequest{Token: ts.Generate().Value, Hostname: "host-a", OS: "linux", Arch: "amd64"})
	resp2 := register(RegisterRequest{Token: ts.Generate().Value, Hostname: "host-b", OS: "linux", Arch: "amd64"})

	if resp1.ProbeID == resp2.ProbeID {
		t.Fatalf("expected different probe ids for different hostnames, both were %s", resp1.ProbeID)
	}
	if len(fm.List()) != 2 {
		t.Fatalf("expected two fleet entries, got %d", len(fm.List()))
	}
}

func TestRegisterHandler_InvalidToken(t *testing.T) {
	ts := newTestTokenStore(t)
	fm := fleet.NewManager(testLogger())
	handler := HandleRegister(ts, fm, testLogger())

	reqBody := RegisterRequest{
		Token:    "invalid-token",
		Hostname: "evil-host",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRegisterHandler_ReusedToken(t *testing.T) {
	ts := newTestTokenStore(t)
	fm := fleet.NewManager(testLogger())
	handler := HandleRegister(ts, fm, testLogger())

	token := ts.Generate()

	// First registration
	reqBody := RegisterRequest{Token: token.Value, Hostname: "host-1"}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first registration failed: %d", w.Code)
	}

	// Second registration with same token
	body2, _ := json.Marshal(reqBody)
	req2 := httptest.NewRequest("POST", "/api/v1/register", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	handler(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("token reuse should return 401, got %d", w2.Code)
	}
}

func TestGenerateTokenHandler(t *testing.T) {
	ts := newTestTokenStore(t)
	handler := HandleGenerateToken(ts, testLogger())

	req := httptest.NewRequest("POST", "/api/v1/tokens", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var token Token
	if err := json.NewDecoder(w.Body).Decode(&token); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if token.Value == "" {
		t.Error("empty token value")
	}
}

func TestGenerateTokenHandler_MultiUseQueryParam(t *testing.T) {
	ts := newTestTokenStore(t)
	handler := HandleGenerateToken(ts, testLogger())

	req := httptest.NewRequest("POST", "/api/v1/tokens?multi_use=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var token Token
	if err := json.NewDecoder(w.Body).Decode(&token); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !token.MultiUse {
		t.Fatal("expected token to be multi-use")
	}
	if !ts.Consume(token.Value) || !ts.Consume(token.Value) {
		t.Fatal("expected multi-use token to be consumable multiple times")
	}
}

func TestGenerateTokenWithAuditHandler_MultiUseQueryParam(t *testing.T) {
	ts := newTestTokenStore(t)
	handler := HandleGenerateTokenWithAudit(ts, auditRecorderStub{}, testLogger())

	req := httptest.NewRequest("POST", "/api/v1/tokens?multi_use=true", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var token Token
	if err := json.NewDecoder(w.Body).Decode(&token); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !token.MultiUse {
		t.Fatal("expected token to be multi-use")
	}
}

func TestRegisterWithAuditHandler_ReRegistrationSummary(t *testing.T) {
	ts := newTestTokenStore(t)
	fm := fleet.NewManager(testLogger())
	recorder := &captureAuditRecorder{}
	handler := HandleRegisterWithAudit(ts, fm, recorder, testLogger())

	register := func(host string) RegisterResponse {
		t.Helper()
		reqBody := RegisterRequest{
			Token:    ts.Generate().Value,
			Hostname: host,
			OS:       "linux",
			Arch:     "amd64",
		}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/api/v1/register", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var resp RegisterResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp
	}

	resp1 := register("audit-host")
	resp2 := register("audit-host")

	if resp1.ProbeID != resp2.ProbeID {
		t.Fatalf("expected same probe id on re-register, got %s and %s", resp1.ProbeID, resp2.ProbeID)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("expected 2 audit events, got %d", len(recorder.events))
	}
	if recorder.events[0].Summary != "Probe registered: audit-host" {
		t.Fatalf("unexpected first summary: %s", recorder.events[0].Summary)
	}
	if recorder.events[1].Summary != "Probe re-registered: audit-host" {
		t.Fatalf("unexpected second summary: %s", recorder.events[1].Summary)
	}
}

func TestGenerateAPIKey(t *testing.T) {
	k1, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	k2, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key second call: %v", err)
	}

	if len(k1) != 68 { // lgk_ + 64 hex chars
		t.Fatalf("unexpected key length: got %d key=%q", len(k1), k1)
	}
	if len(k2) != 68 {
		t.Fatalf("unexpected key length: got %d key=%q", len(k2), k2)
	}
	if k1[:4] != "lgk_" || k2[:4] != "lgk_" {
		t.Fatalf("keys must use lgk_ prefix: %q %q", k1, k2)
	}
	if k1 == k2 {
		t.Fatal("expected unique keys from two generations")
	}
}

func TestListActiveTokens(t *testing.T) {
	ts := newTestTokenStore(t)

	// No tokens initially
	if got := len(ts.ListActive()); got != 0 {
		t.Errorf("expected 0 active tokens, got %d", got)
	}

	// Generate two tokens
	t1 := ts.Generate()
	_ = ts.Generate()
	if got := len(ts.ListActive()); got != 2 {
		t.Errorf("expected 2 active tokens, got %d", got)
	}

	// Consume one
	ts.Consume(t1.Value)
	if got := len(ts.ListActive()); got != 1 {
		t.Errorf("expected 1 active token after consume, got %d", got)
	}

	// Total should still be 2
	if got := ts.Count(); got != 2 {
		t.Errorf("expected 2 total tokens, got %d", got)
	}
}

func TestTokenStorePersistsAcrossCloseReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tokens.db")

	ts, err := NewTokenStore(dbPath)
	if err != nil {
		t.Fatalf("new token store: %v", err)
	}

	single := ts.Generate()
	multi := ts.GenerateWithOptions(GenerateOptions{MultiUse: true})
	if !ts.Consume(single.Value) {
		t.Fatal("expected single-use token consume to succeed")
	}
	if !ts.Consume(multi.Value) {
		t.Fatal("expected multi-use token consume to succeed")
	}

	if err := ts.Close(); err != nil {
		t.Fatalf("close token store: %v", err)
	}

	reopened, err := NewTokenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen token store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if got := reopened.Count(); got != 2 {
		t.Fatalf("expected 2 total tokens after reopen, got %d", got)
	}
	if got := len(reopened.ListActive()); got != 1 {
		t.Fatalf("expected 1 active token after reopen, got %d", got)
	}
	if reopened.Consume(single.Value) {
		t.Fatal("single-use token should remain consumed after reopen")
	}
	if !reopened.Consume(multi.Value) {
		t.Fatal("expected multi-use token consume to succeed after reopen")
	}
	if !reopened.Consume(multi.Value) {
		t.Fatal("expected multi-use token to be consumable repeatedly after reopen")
	}
}

func TestTokenStoreMultiUseAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tokens.db")

	ts, err := NewTokenStore(dbPath)
	if err != nil {
		t.Fatalf("new token store: %v", err)
	}

	token := ts.GenerateWithOptions(GenerateOptions{MultiUse: true})
	if err := ts.Close(); err != nil {
		t.Fatalf("close token store: %v", err)
	}

	reopened, err := NewTokenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen token store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if !reopened.Consume(token.Value) {
		t.Fatal("expected first consume to succeed after reopen")
	}
	if !reopened.Consume(token.Value) {
		t.Fatal("expected second consume to succeed after reopen for multi-use token")
	}
}

func TestTokenStoreListActiveSkipsExpiredAfterReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tokens.db")

	ts, err := NewTokenStore(dbPath)
	if err != nil {
		t.Fatalf("new token store: %v", err)
	}

	token := ts.Generate()
	expiredAt := time.Now().UTC().Add(-1 * time.Minute)
	if _, err := ts.db.Exec("UPDATE tokens SET expires_at = ? WHERE value = ?", expiredAt.Format(time.RFC3339Nano), token.Value); err != nil {
		t.Fatalf("expire token in db: %v", err)
	}

	if err := ts.Close(); err != nil {
		t.Fatalf("close token store: %v", err)
	}

	reopened, err := NewTokenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen token store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if got := reopened.Count(); got != 1 {
		t.Fatalf("expected total count 1 after reopen, got %d", got)
	}
	if got := len(reopened.ListActive()); got != 0 {
		t.Fatalf("expected no active tokens after reopen when expired, got %d", got)
	}
	if reopened.Consume(token.Value) {
		t.Fatal("expected consume to fail for expired token after reopen")
	}
}

func TestHandleListTokens(t *testing.T) {
	ts := newTestTokenStore(t)
	ts.Generate()
	ts.Generate()

	handler := HandleListTokens(ts)
	req := httptest.NewRequest("GET", "/api/v1/tokens", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Tokens []*Token `json:"tokens"`
		Count  int      `json:"count"`
		Total  int      `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("expected count 2, got %d", resp.Count)
	}
	if resp.Total != 2 {
		t.Errorf("expected total 2, got %d", resp.Total)
	}
}

func TestTokenInstallCommand(t *testing.T) {
	ts := newTestTokenStore(t)
	ts.SetServerURL("https://legator.example.com")

	token := ts.Generate()
	if token.InstallCommand == "" {
		t.Fatal("install_command should be set when server URL is configured")
	}
	if !strings.Contains(token.InstallCommand, "https://legator.example.com") {
		t.Errorf("install_command should contain server URL, got: %s", token.InstallCommand)
	}
	if !strings.Contains(token.InstallCommand, token.Value) {
		t.Errorf("install_command should contain token value, got: %s", token.InstallCommand)
	}
}

func TestTokenInstallCommandEmpty(t *testing.T) {
	ts := newTestTokenStore(t)
	// No server URL set

	token := ts.Generate()
	if token.InstallCommand != "" {
		t.Errorf("install_command should be empty when no server URL, got: %s", token.InstallCommand)
	}
}

func TestGenerateTokenHandlerAddsInstallCommandFromRequestHost(t *testing.T) {
	ts := newTestTokenStore(t) // no configured external URL
	handler := HandleGenerateToken(ts, testLogger())

	req := httptest.NewRequest("POST", "/api/v1/tokens", nil)
	req.Host = "legator.test:8080"
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var token Token
	if err := json.NewDecoder(w.Body).Decode(&token); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if token.InstallCommand == "" {
		t.Fatal("expected install_command to be populated from request host")
	}
	if !strings.Contains(token.InstallCommand, "http://legator.test:8080/install.sh") {
		t.Fatalf("unexpected install_command: %s", token.InstallCommand)
	}
}

func TestHandleListTokensAddsInstallCommandFromRequestHost(t *testing.T) {
	ts := newTestTokenStore(t) // no configured external URL
	ts.Generate()

	handler := HandleListTokens(ts)
	req := httptest.NewRequest("GET", "/api/v1/tokens", nil)
	req.Host = "cp.local"
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Tokens []Token `json:"tokens"`
		Count  int     `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 || len(resp.Tokens) != 1 {
		t.Fatalf("expected one token in response, got count=%d len=%d", resp.Count, len(resp.Tokens))
	}
	if resp.Tokens[0].InstallCommand == "" {
		t.Fatal("expected install_command in listed token")
	}
	if !strings.Contains(resp.Tokens[0].InstallCommand, "http://cp.local/install.sh") {
		t.Fatalf("unexpected install_command: %s", resp.Tokens[0].InstallCommand)
	}
}

func TestNoExpiryToken(t *testing.T) {
	ts := newTestTokenStore(t)
	token := ts.GenerateWithOptions(GenerateOptions{MultiUse: true, NoExpiry: true})
	if token == nil {
		t.Fatal("expected token, got nil")
	}
	minExpiry := time.Now().Add(99 * 365 * 24 * time.Hour)
	if token.Expires.Before(minExpiry) {
		t.Fatalf("expected expiry >99 years from now, got %v", token.Expires)
	}
	if !ts.Consume(token.Value) {
		t.Fatal("expected no-expiry token to be consumable")
	}
	if !ts.Consume(token.Value) {
		t.Fatal("expected multi-use no-expiry token to be consumable again")
	}
}
