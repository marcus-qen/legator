package providerproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type auditCapture struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (a *auditCapture) RecordProviderProxyEvent(_ context.Context, evt AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, evt)
}

func (a *auditCapture) all() []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

func validTokenValidator() TokenValidator {
	return TokenValidatorFunc(func(context.Context, TokenValidationRequest) (*TokenClaims, error) {
		return &TokenClaims{RunID: "run-123", SessionID: "sess-1", Issuer: "runner-actor"}, nil
	})
}

func TestProxyRejectsInvalidTokenWithoutProviderCall(t *testing.T) {
	var providerCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ignored"}`))
	}))
	defer provider.Close()

	spend, err := NewSpendStore(t.TempDir() + "/spend.db")
	if err != nil {
		t.Fatalf("new spend store: %v", err)
	}
	defer spend.Close()

	proxy, err := New(ProxyConfig{
		TokenValidator: TokenValidatorFunc(func(context.Context, TokenValidationRequest) (*TokenClaims, error) {
			return nil, ErrUnauthorized
		}),
		Credentials: CredentialResolverFunc(func(context.Context, string, string) (ProviderCredentials, error) {
			return ProviderCredentials{BaseURL: provider.URL, APIKey: "sk-test", Model: "gpt-4o-mini"}, nil
		}),
		SpendStore: spend,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"run_token":"bad","session_id":"sess-1","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	proxy.HandleHTTP(rr, req, "run-123")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
	}
	if providerCalls != 0 {
		t.Fatalf("expected provider not called, got %d", providerCalls)
	}
}

func TestProxySanitizesCredentialFromErrorResponse(t *testing.T) {
	const secret = "sk-super-secret-token"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key: Bearer ` + secret + `"}`))
	}))
	defer provider.Close()

	spend, err := NewSpendStore(t.TempDir() + "/spend.db")
	if err != nil {
		t.Fatalf("new spend store: %v", err)
	}
	defer spend.Close()

	proxy, err := New(ProxyConfig{
		TokenValidator: validTokenValidator(),
		Credentials: CredentialResolverFunc(func(context.Context, string, string) (ProviderCredentials, error) {
			return ProviderCredentials{BaseURL: provider.URL, APIKey: secret, Model: "gpt-4o-mini"}, nil
		}),
		SpendStore: spend,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"run_token":"ok","session_id":"sess-1","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	proxy.HandleHTTP(rr, req, "run-123")

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatalf("expected response body to redact provider key, got %s", rr.Body.String())
	}
}

func TestProxyTracksSpendInSQLite(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"id":    "chatcmpl-1",
			"model": "gpt-4o-mini",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "ok",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 8,
				"total_tokens":      20,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer provider.Close()

	spend, err := NewSpendStore(t.TempDir() + "/spend.db")
	if err != nil {
		t.Fatalf("new spend store: %v", err)
	}
	defer spend.Close()

	proxy, err := New(ProxyConfig{
		TokenValidator: validTokenValidator(),
		Credentials: CredentialResolverFunc(func(context.Context, string, string) (ProviderCredentials, error) {
			return ProviderCredentials{BaseURL: provider.URL, APIKey: "sk-test", Model: "gpt-4o-mini"}, nil
		}),
		SpendStore: spend,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}

	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"run_token":"ok","session_id":"sess-1","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
		proxy.HandleHTTP(rr, req, "run-123")
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d expected 200, got %d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}

	totals, err := spend.Totals(context.Background(), "run-123")
	if err != nil {
		t.Fatalf("query totals: %v", err)
	}
	if totals.TotalTokens != 40 {
		t.Fatalf("expected total tokens 40, got %d", totals.TotalTokens)
	}
	if totals.InputTokens != 24 || totals.OutputTokens != 16 {
		t.Fatalf("unexpected token breakdown input=%d output=%d", totals.InputTokens, totals.OutputTokens)
	}
	if totals.EstimatedCost <= 0 {
		t.Fatalf("expected positive estimated cost, got %f", totals.EstimatedCost)
	}
}

func TestProxyEnforcesTokenRateLimitBeforeProviderCall(t *testing.T) {
	var providerCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer provider.Close()

	spend, err := NewSpendStore(t.TempDir() + "/spend.db")
	if err != nil {
		t.Fatalf("new spend store: %v", err)
	}
	defer spend.Close()

	if _, err := spend.Record(context.Background(), SpendRecord{RunID: "run-123", InputTokens: 60, OutputTokens: 40, TotalTokens: 100}); err != nil {
		t.Fatalf("seed spend: %v", err)
	}

	proxy, err := New(ProxyConfig{
		TokenValidator: validTokenValidator(),
		Credentials: CredentialResolverFunc(func(context.Context, string, string) (ProviderCredentials, error) {
			return ProviderCredentials{BaseURL: provider.URL, APIKey: "sk-test", Model: "gpt-4o-mini"}, nil
		}),
		SpendStore:      spend,
		MaxTokensPerRun: 100,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"run_token":"ok","session_id":"sess-1","model":"gpt-4o-mini","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	proxy.HandleHTTP(rr, req, "run-123")

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rr.Code, rr.Body.String())
	}
	if providerCalls != 0 {
		t.Fatalf("expected provider not called, got %d", providerCalls)
	}
}

func TestProxyEmitsAuditEvent(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-mini","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer provider.Close()

	spend, err := NewSpendStore(t.TempDir() + "/spend.db")
	if err != nil {
		t.Fatalf("new spend store: %v", err)
	}
	defer spend.Close()

	audit := &auditCapture{}
	proxy, err := New(ProxyConfig{
		TokenValidator: validTokenValidator(),
		Credentials: CredentialResolverFunc(func(context.Context, string, string) (ProviderCredentials, error) {
			return ProviderCredentials{BaseURL: provider.URL, APIKey: "sk-test", Model: "gpt-4o-mini"}, nil
		}),
		SpendStore: spend,
		AuditSink:  audit,
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"run_token":"ok","session_id":"sess-1","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	proxy.HandleHTTP(rr, req, "run-123")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	events := audit.all()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].RunID != "run-123" {
		t.Fatalf("expected run_id run-123, got %q", events[0].RunID)
	}
	if events[0].Actor != "runner-actor" {
		t.Fatalf("expected actor runner-actor, got %q", events[0].Actor)
	}
	if events[0].TotalTokens != 8 {
		t.Fatalf("expected token count 8, got %d", events[0].TotalTokens)
	}
}
