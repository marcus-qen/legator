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

func makeDashboardServer(t *testing.T, objects ...client.Object) *Server {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	srv, err := NewServer(cl, Config{}, logr.Discard())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

func withTestUser(r *http.Request, email string) *http.Request {
	ctx := context.WithValue(r.Context(), userContextKey, &OIDCUser{Email: email})
	return r.WithContext(ctx)
}

func testAgentForRunTrigger(agentName, namespace string, ann map[string]string) *corev1alpha1.LegatorAgent {
	a := &corev1alpha1.LegatorAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name:        agentName,
			Namespace:   namespace,
			Annotations: ann,
		},
	}
	a.SetCreationTimestamp(metav1.NewTime(time.Now().UTC().Add(-time.Hour)))
	return a
}

func TestHandleAgentRunTriggerSetsAnnotationsAndRedirects(t *testing.T) {
	agent := testAgentForRunTrigger("db-checker", "agents", nil)
	srv := makeDashboardServer(t, agent)

	body := url.Values{}
	body.Set("target", "prod-db-01")
	body.Set("task", "check disk health")
	req := httptest.NewRequest(http.MethodPost, "/agents/db-checker/run", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withTestUser(req, "operator@example.com")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got, want := rr.Header().Get("Location"), "/agents/db-checker?triggered=1"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	ref := &corev1alpha1.LegatorAgent{}
	if err := srv.client.Get(context.Background(), client.ObjectKey{Name: "db-checker", Namespace: "agents"}, ref); err != nil {
		t.Fatalf("get agent: %v", err)
	}

	ann := ref.Annotations
	if got, want := ann["legator.io/run-now"], "true"; got != want {
		t.Fatalf("run-now = %q, want %q", got, want)
	}
	if got := ann["legator.io/task"]; got != "check disk health" {
		t.Fatalf("task annotation = %q", got)
	}
	if got := ann["legator.io/target"]; got != "prod-db-01" {
		t.Fatalf("target annotation = %q", got)
	}
	if got := ann["legator.io/triggered-by"]; got != "operator@example.com" {
		t.Fatalf("triggered-by = %q", got)
	}
}

func TestHandleAgentRunTriggerClearsTaskAndTargetWhenBlank(t *testing.T) {
	agent := testAgentForRunTrigger("web-scanner", "agents", map[string]string{
		"legator.io/task":         "old task",
		"legator.io/target":       "old-target",
		"legator.io/triggered-by": "old@example.com",
	})
	srv := makeDashboardServer(t, agent)

	body := url.Values{}
	body.Set("target", "")
	body.Set("task", "")
	req := httptest.NewRequest(http.MethodPost, "/agents/web-scanner/run", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withTestUser(req, "ops@example.com")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}

	ref := &corev1alpha1.LegatorAgent{}
	if err := srv.client.Get(context.Background(), client.ObjectKey{Name: "web-scanner", Namespace: "agents"}, ref); err != nil {
		t.Fatalf("get agent: %v", err)
	}

	if _, ok := ref.Annotations["legator.io/task"]; ok {
		t.Fatalf("expected task annotation to be cleared")
	}
	if _, ok := ref.Annotations["legator.io/target"]; ok {
		t.Fatalf("expected target annotation to be cleared")
	}
	if got := ref.Annotations["legator.io/triggered-by"]; got != "ops@example.com" {
		t.Fatalf("triggered-by = %q, want ops@example.com", got)
	}
	if got := ref.Annotations["legator.io/run-now"]; got != "true" {
		t.Fatalf("run-now = %q, want true", got)
	}
}

func TestHandleAgentRunTriggerRejectsGetMethod(t *testing.T) {
	srv := makeDashboardServer(t, testAgentForRunTrigger("audit-agent", "agents", nil))

	req := httptest.NewRequest(http.MethodGet, "/agents/audit-agent/run", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}
