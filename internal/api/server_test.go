package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
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

func newUserPolicyClient(t *testing.T, objs ...*corev1alpha1.UserPolicy) *fake.ClientBuilder {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	objects := make([]runtime.Object, 0, len(objs))
	for _, obj := range objs {
		objects = append(objects, obj)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...)
}

func newTestClient(t *testing.T, objs ...runtime.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
}

type blockingApprovalsClient struct {
	client.Client
}

func (b *blockingApprovalsClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch list.(type) {
	case *corev1alpha1.ApprovalRequestList:
		<-ctx.Done()
		return ctx.Err()
	default:
		return b.Client.List(ctx, list, opts...)
	}
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

func TestWhoAmI_UserPolicyRestrictsOperator(t *testing.T) {
	k8s := newUserPolicyClient(t, &corev1alpha1.UserPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "restrict-operator"},
		Spec: corev1alpha1.UserPolicySpec{
			Subjects: []corev1alpha1.UserPolicySubject{{Claim: "email", Value: "operator@example.com"}},
			Role:     corev1alpha1.UserPolicyRoleViewer,
		},
	}).Build()

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{
			{
				Name:     "rbac-operator",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "operator@example.com"}},
				Role:     rbac.RoleOperator,
			},
		},
	}, k8s, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "operator-1",
		"email": "operator@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
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

	if got := body["effectiveRole"]; got != "viewer" {
		t.Fatalf("effectiveRole = %v, want viewer", got)
	}
}

func TestWhoAmI_UserPolicyCannotBypassViewer(t *testing.T) {
	k8s := newUserPolicyClient(t, &corev1alpha1.UserPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "admin-override-attempt"},
		Spec: corev1alpha1.UserPolicySpec{
			Subjects: []corev1alpha1.UserPolicySubject{{Claim: "email", Value: "viewer@example.com"}},
			Role:     corev1alpha1.UserPolicyRoleAdmin,
		},
	}).Build()

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{
			{
				Name:     "rbac-viewer",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
				Role:     rbac.RoleViewer,
			},
		},
	}, k8s, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
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

	if got := body["effectiveRole"]; got != "viewer" {
		t.Fatalf("effectiveRole = %v, want viewer", got)
	}

	perms, ok := body["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions missing or invalid")
	}
	cfgPerm, ok := perms[string(rbac.ActionConfigure)].(map[string]any)
	if !ok {
		t.Fatalf("config permission missing")
	}
	if allowed, _ := cfgPerm["allowed"].(bool); allowed {
		t.Fatalf("expected config:write denied despite admin userpolicy")
	}
}

func TestListApprovals_ReturnsAgentsNamespaceOnly(t *testing.T) {
	k8s := newTestClient(t,
		&corev1alpha1.ApprovalRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "approve-in-agents", Namespace: "agents"},
			Spec: corev1alpha1.ApprovalRequestSpec{
				AgentName: "watchman",
				RunName:   "run-1",
				Action: corev1alpha1.ProposedAction{
					Tool:        "ssh.exec",
					Tier:        "destructive",
					Target:      "castra",
					Description: "restart service",
				},
			},
		},
		&corev1alpha1.ApprovalRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "approve-in-other", Namespace: "other"},
			Spec: corev1alpha1.ApprovalRequestSpec{
				AgentName: "watchman",
				RunName:   "run-2",
				Action: corev1alpha1.ProposedAction{
					Tool:        "ssh.exec",
					Tier:        "destructive",
					Target:      "principia",
					Description: "restart service",
				},
			},
		},
	)

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{
			{
				Name:     "operator",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "operator@example.com"}},
				Role:     rbac.RoleOperator,
			},
		},
	}, k8s, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "operator-1",
		"email": "operator@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest("GET", "/api/v1/approvals", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Approvals []corev1alpha1.ApprovalRequest `json:"approvals"`
		Total     int                            `json:"total"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Total != 1 {
		t.Fatalf("total = %d, want 1", body.Total)
	}
	if len(body.Approvals) != 1 {
		t.Fatalf("approvals length = %d, want 1", len(body.Approvals))
	}
	if got := body.Approvals[0].Namespace; got != "agents" {
		t.Fatalf("approval namespace = %q, want agents", got)
	}
	if got := body.Approvals[0].Name; got != "approve-in-agents" {
		t.Fatalf("approval name = %q, want approve-in-agents", got)
	}
}

func TestListApprovals_FailsFastOnTimeout(t *testing.T) {
	baseClient := newTestClient(t)
	k8s := &blockingApprovalsClient{Client: baseClient}

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{
			{
				Name:     "operator",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "operator@example.com"}},
				Role:     rbac.RoleOperator,
			},
		},
	}, k8s, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "operator-1",
		"email": "operator@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest("GET", "/api/v1/approvals", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	start := time.Now()
	srv.Handler().ServeHTTP(rr, req)
	duration := time.Since(start)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	if duration > 7*time.Second {
		t.Fatalf("duration = %s, expected fail-fast timeout", duration)
	}
	if !strings.Contains(rr.Body.String(), "context deadline exceeded") {
		t.Fatalf("expected timeout error, got body: %s", rr.Body.String())
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

func TestAuditTrailIncludesSafetyOutcomeSummary(t *testing.T) {
	scheme := newTestScheme(t)
	run := &corev1alpha1.LegatorRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "run-1",
			Namespace:         "agents",
			Labels:            map[string]string{"legator.io/agent": "forge"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
		},
		Spec: corev1alpha1.LegatorRunSpec{
			AgentRef:       "forge",
			EnvironmentRef: "prod",
			Trigger:        corev1alpha1.RunTriggerManual,
		},
		Status: corev1alpha1.LegatorRunStatus{
			Phase: corev1alpha1.RunPhaseBlocked,
			Actions: []corev1alpha1.ActionRecord{
				{Status: corev1alpha1.ActionStatusBlocked, PreFlightCheck: &corev1alpha1.PreFlightResult{SafetyGateOutcome: "BLOCKED"}},
				{Status: corev1alpha1.ActionStatusDenied, PreFlightCheck: &corev1alpha1.PreFlightResult{ApprovalCheck: "REQUIRED", ApprovalDecision: "DENIED", SafetyGateOutcome: "DENIED"}},
				{Status: corev1alpha1.ActionStatusApproved, PreFlightCheck: &corev1alpha1.PreFlightResult{ApprovalCheck: "REQUIRED", ApprovalDecision: "APPROVED", SafetyGateOutcome: "APPROVED"}},
			},
			Report: "safety summary",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(run).Build()

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{{
			Name:     "viewer-policy",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
			Role:     rbac.RoleViewer,
		}},
	}, c, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
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
	entries, ok := body["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries malformed: %#v", body["entries"])
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("entry malformed: %#v", entries[0])
	}
	if got := int(entry["safetyBlocked"].(float64)); got != 2 {
		t.Fatalf("safetyBlocked = %d, want 2", got)
	}
	if got := int(entry["approvalsRequired"].(float64)); got != 2 {
		t.Fatalf("approvalsRequired = %d, want 2", got)
	}
	if got := int(entry["approvalsApproved"].(float64)); got != 1 {
		t.Fatalf("approvalsApproved = %d, want 1", got)
	}
	if got := int(entry["approvalsDenied"].(float64)); got != 1 {
		t.Fatalf("approvalsDenied = %d, want 1", got)
	}
}

func TestListAnomalies_ReturnsOnlyAnomalyEvents(t *testing.T) {
	k8s := newTestClient(t,
		&corev1alpha1.AgentEvent{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "anomaly-1",
				Namespace:         "agents",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
			},
			Spec: corev1alpha1.AgentEventSpec{
				SourceAgent: "watchman",
				SourceRun:   "run-123",
				EventType:   "anomaly",
				Severity:    corev1alpha1.EventSeverityWarning,
				Summary:     "frequency anomaly",
				Detail:      "7 manual runs in 30m",
			},
		},
		&corev1alpha1.AgentEvent{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "finding-1",
				Namespace:         "agents",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Minute)),
			},
			Spec: corev1alpha1.AgentEventSpec{
				SourceAgent: "watchman",
				EventType:   "finding",
				Severity:    corev1alpha1.EventSeverityInfo,
				Summary:     "normal finding",
			},
		},
	)

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{
			{
				Name:     "viewer",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
				Role:     rbac.RoleViewer,
			},
		},
	}, k8s, logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest("GET", "/api/v1/anomalies", nil)
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

	if got := body["total"]; got != float64(1) {
		t.Fatalf("total = %v, want 1", got)
	}
	entries, ok := body["anomalies"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("unexpected anomalies payload: %#v", body["anomalies"])
	}
}

func TestPolicySimulation_ProjectedPolicyRestrictsRole(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{
			{
				Name:     "operator",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "operator@example.com"}},
				Role:     rbac.RoleOperator,
			},
		},
	}, newTestClient(t), logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":    "operator-1",
		"email":  "operator@example.com",
		"groups": []string{"legator-operator"},
		"exp":    float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	payload := map[string]any{
		"actions":   []string{"agents:run"},
		"resources": []string{"watchman-deep"},
		"proposedPolicy": map[string]any{
			"name": "restrict-runner",
			"role": "viewer",
			"subjects": []map[string]any{
				{"claim": "email", "value": "operator@example.com"},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/policy/simulate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	evals, ok := resp["evaluations"].([]any)
	if !ok || len(evals) != 1 {
		t.Fatalf("unexpected evaluations payload: %#v", resp["evaluations"])
	}
	entry := evals[0].(map[string]any)
	current := entry["current"].(map[string]any)
	projected := entry["projected"].(map[string]any)
	if allowed, _ := current["allowed"].(bool); !allowed {
		t.Fatalf("expected current decision to allow run")
	}
	if allowed, _ := projected["allowed"].(bool); allowed {
		t.Fatalf("expected projected decision to deny run")
	}
}

func TestPolicySimulation_RateLimitProjection(t *testing.T) {
	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{
			{
				Name:     "operator",
				Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "operator@example.com"}},
				Role:     rbac.RoleOperator,
			},
		},
	}, newTestClient(t), logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "operator-1",
		"email": "operator@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	payload := map[string]any{
		"actions":            []string{"agents:run"},
		"resources":          []string{"watchman-deep"},
		"runRatePerHour":     999,
		"requestRatePerHour": 999,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/policy/simulate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	evals := resp["evaluations"].([]any)
	entry := evals[0].(map[string]any)
	projected := entry["projected"].(map[string]any)
	rateLimit := projected["rateLimit"].(map[string]any)
	if allowed, _ := rateLimit["allowed"].(bool); allowed {
		t.Fatalf("expected projected rate limit deny")
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

	for i := range 3 {
		req := httptest.NewRequest("GET", "/healthz", nil)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("healthz request %d status = %d, want %d", i+1, rr.Code, http.StatusOK)
		}
	}
}
