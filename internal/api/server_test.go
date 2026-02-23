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
	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/api/auth"
	"github.com/marcus-qen/legator/internal/api/rbac"
	"github.com/marcus-qen/legator/internal/inventory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func newTestAPIServerWithObjects(objects ...client.Object) *Server {
	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		panic("failed to add scheme")
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return NewServer(ServerConfig{
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
	}, k8sClient, logr.Discard())
}

func viewerJWT(t *testing.T) string {
	t.Helper()
	return makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})
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

func TestMCPRunSurfaceReturnsLatestRunSummary(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	srv := newTestAPIServerWithObjects(
		&corev1alpha1.LegatorRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "run-old",
				Namespace:         "agents",
				CreationTimestamp: metav1.Time{Time: base.Add(-60 * time.Minute)},
			},
			Spec: corev1alpha1.LegatorRunSpec{
				AgentRef:       "agent-a",
				EnvironmentRef: "env-a",
				Trigger:        corev1alpha1.RunTriggerScheduled,
				ModelUsed:      "small",
			},
			Status: corev1alpha1.LegatorRunStatus{Phase: corev1alpha1.RunPhaseSucceeded},
		},
		&corev1alpha1.LegatorRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "run-new",
				Namespace:         "agents",
				CreationTimestamp: metav1.Time{Time: base.Add(-5 * time.Minute)},
			},
			Spec: corev1alpha1.LegatorRunSpec{
				AgentRef:       "agent-a",
				EnvironmentRef: "env-a",
				Trigger:        corev1alpha1.RunTriggerManual,
			},
			Status: corev1alpha1.LegatorRunStatus{
				Phase:          corev1alpha1.RunPhaseRunning,
				CompletionTime: &metav1.Time{Time: base.Add(-2 * time.Minute)},
			},
		},
	)

	req := httptest.NewRequest("GET", "/api/v1/mcp/run?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+viewerJWT(t))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["surface"]; got != "run" {
		t.Fatalf("surface = %v, want run", got)
	}
	runsAny, ok := body["runs"].([]any)
	if !ok {
		t.Fatal("runs missing or invalid")
	}
	if len(runsAny) != 1 {
		t.Fatalf("runs len = %d, want 1", len(runsAny))
	}
	first := runsAny[0].(map[string]any)
	if got := first["name"]; got != "run-new" {
		t.Fatalf("run name = %v, want run-new", got)
	}
}

func TestMCPCheckSurfaceReturnsAgentState(t *testing.T) {
	srv := newTestAPIServerWithObjects(&corev1alpha1.LegatorAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-a",
			Namespace: "agents",
		},
		Spec: corev1alpha1.LegatorAgentSpec{
			EnvironmentRef: "env-a",
			Guardrails:     corev1alpha1.GuardrailsSpec{Autonomy: corev1alpha1.AutonomyObserve},
			Skills:         []corev1alpha1.SkillRef{{Name: "noop", Source: "builtin"}},
			Model:          corev1alpha1.ModelSpec{Tier: corev1alpha1.ModelTierStandard},
		},
		Status: corev1alpha1.LegatorAgentStatus{Phase: corev1alpha1.LegatorAgentPhaseReady},
	})

	req := httptest.NewRequest("GET", "/api/v1/mcp/check?agent=agent-a", nil)
	req.Header.Set("Authorization", "Bearer "+viewerJWT(t))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["surface"]; got != "check" {
		t.Fatalf("surface = %v, want check", got)
	}
	agent, ok := body["agent"].(map[string]any)
	if !ok {
		t.Fatal("agent missing or invalid")
	}
	if got := agent["name"]; got != "agent-a" {
		t.Fatalf("agent.name = %v, want agent-a", got)
	}
}

func TestMCPStatusSurfaceReturnsRunSummary(t *testing.T) {
	srv := newTestAPIServerWithObjects(
		&corev1alpha1.LegatorRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "run-check",
				Namespace:         "agents",
				CreationTimestamp: metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
			},
			Spec: corev1alpha1.LegatorRunSpec{
				AgentRef:       "agent-a",
				EnvironmentRef: "env-a",
				Trigger:        corev1alpha1.RunTriggerWebhook,
			},
			Status: corev1alpha1.LegatorRunStatus{Phase: corev1alpha1.RunPhaseSucceeded},
		},
	)

	req := httptest.NewRequest("GET", "/api/v1/mcp/status?run=run-check", nil)
	req.Header.Set("Authorization", "Bearer "+viewerJWT(t))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["surface"]; got != "status" {
		t.Fatalf("surface = %v, want status", got)
	}
	if got := body["name"]; got != "run-check" {
		t.Fatalf("name = %v, want run-check", got)
	}
}

func TestMCPInventorySurfaceUsesInventoryProvider(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{
			BypassPaths: []string{"/healthz"},
		},
		Policies: []rbac.UserPolicy{{
			Name:     "viewer",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
			Role:     rbac.RoleViewer,
		}},
		Inventory: &fakeInventoryProvider{
			devices: []inventory.ManagedDevice{{
				Name: "node-1",
			}},
			sync: map[string]any{
				"provider": "fake",
			},
		},
	}, nil, logr.Discard())

	req := httptest.NewRequest("GET", "/api/v1/mcp/inventory", nil)
	req.Header.Set("Authorization", "Bearer "+viewerJWT(t))
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
	if got, ok := body["total"].(float64); !ok || got != 1 {
		t.Fatalf("total = %v, want 1", body["total"])
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
