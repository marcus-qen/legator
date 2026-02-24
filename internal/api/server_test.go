package api

import (
	"bytes"
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
	"github.com/marcus-qen/legator/internal/approval"
	"github.com/marcus-qen/legator/internal/inventory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

func newApprovalTestServer(t *testing.T, objects ...runtime.Object) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.ApprovalRequest{}).WithRuntimeObjects(objects...).Build()

	return NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{{
			Name:     "operator-policy",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "operator@example.com"}},
			Role:     rbac.RoleOperator,
		}},
	}, c, logr.Discard())
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

func TestApprovalDecision_TypedConfirmationRequired(t *testing.T) {
	exp := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	ar := &corev1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "approval-typed-required",
			Namespace: "agents",
			Annotations: map[string]string{
				approval.AnnotationTypedConfirmationRequired:  "true",
				approval.AnnotationTypedConfirmationToken:     "CONFIRM-AAAA1111",
				approval.AnnotationTypedConfirmationExpiresAt: exp,
			},
		},
		Status: corev1alpha1.ApprovalRequestStatus{Phase: corev1alpha1.ApprovalPhasePending},
	}
	srv := newApprovalTestServer(t, ar)

	token := makeTestJWT(map[string]any{
		"sub":   "operator-1",
		"email": "operator@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	payload := []byte(`{"decision":"approve","reason":"looks good"}`)
	req := httptest.NewRequest("POST", "/api/v1/approvals/approval-typed-required", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestApprovalDecision_TypedConfirmationMismatch(t *testing.T) {
	exp := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	ar := &corev1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "approval-typed-mismatch",
			Namespace: "agents",
			Annotations: map[string]string{
				approval.AnnotationTypedConfirmationRequired:  "true",
				approval.AnnotationTypedConfirmationToken:     "CONFIRM-BBBB2222",
				approval.AnnotationTypedConfirmationExpiresAt: exp,
			},
		},
		Status: corev1alpha1.ApprovalRequestStatus{Phase: corev1alpha1.ApprovalPhasePending},
	}
	srv := newApprovalTestServer(t, ar)

	token := makeTestJWT(map[string]any{
		"sub":   "operator-1",
		"email": "operator@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	payload := []byte(`{"decision":"approve","reason":"looks good","typedConfirmation":"CONFIRM-WRONG"}`)
	req := httptest.NewRequest("POST", "/api/v1/approvals/approval-typed-mismatch", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestApprovalDecision_TypedConfirmationAccepted(t *testing.T) {
	exp := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	ar := &corev1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "approval-typed-ok",
			Namespace: "agents",
			Annotations: map[string]string{
				approval.AnnotationTypedConfirmationRequired:  "true",
				approval.AnnotationTypedConfirmationToken:     "CONFIRM-CCCC3333",
				approval.AnnotationTypedConfirmationExpiresAt: exp,
			},
		},
		Status: corev1alpha1.ApprovalRequestStatus{Phase: corev1alpha1.ApprovalPhasePending},
	}
	srv := newApprovalTestServer(t, ar)

	token := makeTestJWT(map[string]any{
		"sub":   "operator-1",
		"email": "operator@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	payload := []byte(`{"decision":"approve","reason":"looks good","typedConfirmation":"CONFIRM-CCCC3333"}`)
	req := httptest.NewRequest("POST", "/api/v1/approvals/approval-typed-ok", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
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
