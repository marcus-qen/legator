package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/marcus-qen/legator/internal/api/auth"
	"github.com/marcus-qen/legator/internal/api/rbac"
	"github.com/marcus-qen/legator/internal/inventory"
)

// makeTestJWT creates a minimal JWT for testing (same as auth package helper).
func makeTestJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf("%s.%s.test-signature", header, body)
}

type fakeInventoryProvider struct {
	devices []inventory.ManagedDevice
	sync    map[string]any
}

func (f *fakeInventoryProvider) Devices() []inventory.ManagedDevice {
	return f.devices
}

func (f *fakeInventoryProvider) InventoryStatus() map[string]any {
	return f.sync
}

func TestInventoryIncludesSyncStatusFromProvider(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{
			BypassPaths: []string{"/healthz"},
		},
		Policies: []rbac.UserPolicy{
			{
				Name:     "viewer",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
				Role:     rbac.RoleViewer,
			},
		},
		Inventory: &fakeInventoryProvider{
			devices: []inventory.ManagedDevice{},
			sync: map[string]any{
				"provider": "headscale",
				"healthy":  true,
			},
		},
	}, nil, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest("GET", "/api/v1/inventory", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["source"]; got != "inventory-provider" {
		t.Fatalf("source = %v, want inventory-provider", got)
	}
	sync, ok := body["sync"].(map[string]any)
	if !ok {
		t.Fatalf("sync field missing or invalid: %#v", body["sync"])
	}
	if got := sync["provider"]; got != "headscale" {
		t.Fatalf("sync.provider = %v, want headscale", got)
	}
}

func TestHealthzBypassesAuth(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{
			BypassPaths: []string{"/healthz"},
		},
	}, nil, logr.Discard())

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]string
	json.NewDecoder(rr.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestUnauthenticatedRequestDenied(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{
			BypassPaths: []string{"/healthz"},
		},
	}, nil, logr.Discard())

	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRBACDeniesViewerFromRunAgent(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{
			BypassPaths: []string{"/healthz"},
		},
		Policies: []rbac.UserPolicy{
			{
				Name:     "viewer-only",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
				Role:     rbac.RoleViewer,
			},
		},
	}, nil, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest("POST", "/api/v1/agents/forge/run", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestWhoAmIReturnsIdentityAndPermissions(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{
			BypassPaths: []string{"/healthz"},
		},
		Policies: []rbac.UserPolicy{
			{
				Name:     "operator-policy",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "operator@example.com"}},
				Role:     rbac.RoleOperator,
			},
		},
	}, nil, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":    "operator-1",
		"email":  "operator@example.com",
		"name":   "Operator One",
		"groups": []string{"legator-operator"},
		"exp":    float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got := body["email"]; got != "operator@example.com" {
		t.Fatalf("email = %v, want operator@example.com", got)
	}
	if got := body["effectiveRole"]; got != "operator" {
		t.Fatalf("effectiveRole = %v, want operator", got)
	}

	perms, ok := body["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions missing or invalid: %#v", body["permissions"])
	}

	runPerm, ok := perms[string(rbac.ActionRunAgent)].(map[string]any)
	if !ok {
		t.Fatalf("run permission missing")
	}
	if allowed, _ := runPerm["allowed"].(bool); !allowed {
		t.Fatalf("expected agents:run to be allowed for operator")
	}

	cfgPerm, ok := perms[string(rbac.ActionConfigure)].(map[string]any)
	if !ok {
		t.Fatalf("config permission missing")
	}
	if allowed, _ := cfgPerm["allowed"].(bool); allowed {
		t.Fatalf("expected config:write to be denied for operator")
	}
}

func TestAuditMiddlewareLogsRequests(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{
			BypassPaths: []string{"/healthz"},
		},
	}, nil, logr.Discard())

	// Just verify it doesn't panic
	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestUserRateLimitBlocksSecondRequestForSameUser(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{{
			Name:     "viewer-policy",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
			Role:     rbac.RoleViewer,
		}},
		UserRateLimit: UserRateLimitConfig{
			Enabled:                 true,
			ViewerRequestsPerMinute: 1,
			ViewerBurst:             1,
		},
	}, nil, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req1 := httptest.NewRequest("GET", "/api/v1/me", nil)
	req1.Header.Set("Authorization", "Bearer "+token)
	rr1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", rr1.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest("GET", "/api/v1/me", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want %d", rr2.Code, http.StatusTooManyRequests)
	}

	var body map[string]any
	if err := json.NewDecoder(rr2.Body).Decode(&body); err != nil {
		t.Fatalf("decode 429 body: %v", err)
	}
	if body["error"] != "rate_limited" {
		t.Fatalf("error field = %v, want rate_limited", body["error"])
	}
}

func TestUserRateLimitIsolatedPerUser(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{{
			Name:     "viewer-policy-a",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer-a@example.com"}},
			Role:     rbac.RoleViewer,
		}, {
			Name:     "viewer-policy-b",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer-b@example.com"}},
			Role:     rbac.RoleViewer,
		}},
		UserRateLimit: UserRateLimitConfig{
			Enabled:                 true,
			ViewerRequestsPerMinute: 1,
			ViewerBurst:             1,
		},
	}, nil, logr.Discard())

	tokenA := makeTestJWT(map[string]any{"sub": "a", "email": "viewer-a@example.com", "exp": float64(time.Now().Add(1 * time.Hour).Unix())})
	tokenB := makeTestJWT(map[string]any{"sub": "b", "email": "viewer-b@example.com", "exp": float64(time.Now().Add(1 * time.Hour).Unix())})

	// Consume viewer A quota
	reqA1 := httptest.NewRequest("GET", "/api/v1/me", nil)
	reqA1.Header.Set("Authorization", "Bearer "+tokenA)
	rrA1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrA1, reqA1)
	if rrA1.Code != http.StatusOK {
		t.Fatalf("viewer A first request status = %d, want %d", rrA1.Code, http.StatusOK)
	}

	// Viewer B should still pass
	reqB := httptest.NewRequest("GET", "/api/v1/me", nil)
	reqB.Header.Set("Authorization", "Bearer "+tokenB)
	rrB := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrB, reqB)
	if rrB.Code != http.StatusOK {
		t.Fatalf("viewer B request status = %d, want %d", rrB.Code, http.StatusOK)
	}
}

func TestUserRateLimitBypassesHealthz(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		UserRateLimit: UserRateLimitConfig{
			Enabled:                 true,
			ViewerRequestsPerMinute: 1,
			ViewerBurst:             1,
		},
	}, nil, logr.Discard())

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/healthz", nil)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("healthz request %d status = %d, want %d", i+1, rr.Code, http.StatusOK)
		}
	}
}
