package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func makeDashboardServerWithStatusSubresource(t *testing.T, objects ...client.Object) *Server {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&corev1alpha1.ApprovalRequest{}).
		Build()

	srv, err := NewServer(cl, Config{}, logr.Discard())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	return srv
}

func approvalForDashboard(name, agent, run string, phase corev1alpha1.ApprovalRequestPhase, age time.Duration) *corev1alpha1.ApprovalRequest {
	return &corev1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "agents",
			CreationTimestamp: metav1.NewTime(
				time.Now().UTC().Add(-age),
			),
		},
		Spec: corev1alpha1.ApprovalRequestSpec{
			AgentName: agent,
			RunName:   run,
			Action: corev1alpha1.ProposedAction{
				Tool:        "kubernetes",
				Tier:        "mutation",
				Description: "Restart ingress after patch",
			},
		},
		Status: corev1alpha1.ApprovalRequestStatus{Phase: phase},
	}
}

func TestHandleApprovalsShowsPendingQueueAndAgentLinks(t *testing.T) {
	srv := makeDashboardServerWithStatusSubresource(
		t,
		approvalForDashboard("apr-pending-new", "agent-alpha", "run-alpha", corev1alpha1.ApprovalPhasePending, 30*time.Minute),
		approvalForDashboard("apr-approved", "agent-bravo", "run-bravo", corev1alpha1.ApprovalPhaseApproved, 20*time.Minute),
		approvalForDashboard("apr-pending-old", "agent-charlie", "run-charlie", corev1alpha1.ApprovalPhasePending, 2*time.Hour),
	)

	req := httptest.NewRequest(http.MethodGet, "/approvals", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "Pending: 2 / 3") {
		t.Fatalf("expected pending counter")
	}
	if !strings.Contains(body, "/agents/agent-alpha") {
		t.Fatalf("expected agent-alpha link")
	}

	idxPendingNew := strings.Index(body, "apr-pending-new")
	idxPendingOld := strings.Index(body, "apr-pending-old")
	idxApproved := strings.Index(body, "apr-approved")
	if idxPendingNew == -1 || idxPendingOld == -1 || idxApproved == -1 {
		t.Fatalf("missing approval rows in response")
	}
	if idxPendingNew > idxPendingOld {
		t.Fatalf("pending queue order wrong: newest pending should appear first")
	}
	if idxApproved < idxPendingOld {
		t.Fatalf("approved request should come after pending queue")
	}

	if !strings.Contains(body, "Deny") {
		t.Fatalf("expected deny action button")
	}
	if !strings.Contains(body, "Approve") {
		t.Fatalf("expected approve action button")
	}
}

func TestHandleApprovalActionApprovesAndStoresReasonAndActor(t *testing.T) {
	approval := approvalForDashboard("apr-approve", "agent-alpha", "run-alpha", corev1alpha1.ApprovalPhasePending, 10*time.Minute)
	srv := makeDashboardServerWithStatusSubresource(t, approval)

	payload := url.Values{}
	payload.Set("reason", "business required")
	req := httptest.NewRequest(http.MethodPost, "/approvals/apr-approve/approve", strings.NewReader(payload.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withTestUser(req, "ops@example.com")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got, want := rr.Header().Get("Location"), "/approvals"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	ref := &corev1alpha1.ApprovalRequest{}
	if err := srv.client.Get(context.Background(), client.ObjectKey{Name: "apr-approve", Namespace: "agents"}, ref); err != nil {
		t.Fatalf("get approval: %v", err)
	}

	if got := ref.Status.Phase; got != corev1alpha1.ApprovalPhaseApproved {
		t.Fatalf("phase = %q, want %q", got, corev1alpha1.ApprovalPhaseApproved)
	}
	if got := ref.Status.Reason; got != "business required" {
		t.Fatalf("reason = %q, want business required", got)
	}
	if got := ref.Status.DecidedBy; got != "ops@example.com" {
		t.Fatalf("decided by = %q, want ops@example.com", got)
	}
	if ref.Status.DecidedAt == nil {
		t.Fatalf("expected DecidedAt")
	}
}

func TestHandleApprovalActionRejectsUnsupportedAction(t *testing.T) {
	approval := approvalForDashboard("apr-bad-action", "agent-alpha", "run-alpha", corev1alpha1.ApprovalPhasePending, time.Minute)
	srv := makeDashboardServerWithStatusSubresource(t, approval)

	req := httptest.NewRequest(http.MethodPost, "/approvals/apr-bad-action/invalid", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}
