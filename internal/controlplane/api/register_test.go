package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestTokenGeneration(t *testing.T) {
	ts := NewTokenStore()

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
	ts := NewTokenStore()
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

func TestTokenInvalid(t *testing.T) {
	ts := NewTokenStore()

	if ts.Consume("nonexistent") {
		t.Error("nonexistent token should fail")
	}
}

func TestRegisterHandler(t *testing.T) {
	ts := NewTokenStore()
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

func TestRegisterHandler_InvalidToken(t *testing.T) {
	ts := NewTokenStore()
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
	ts := NewTokenStore()
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
	ts := NewTokenStore()
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
