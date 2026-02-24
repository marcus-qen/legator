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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func dashboardTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func TestHandleMissionLaunchRequiresAPIBridge(t *testing.T) {
	t.Parallel()

	scheme := dashboardTestScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{client: k8sClient, config: Config{BasePath: ""}, log: logr.Discard()}

	form := url.Values{"agent": []string{"watchman-light"}, "intent": []string{"check"}}
	req := httptest.NewRequest(http.MethodPost, "/cockpit/missions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &OIDCUser{Subject: "u1"}))

	rr := httptest.NewRecorder()
	srv.handleMissionLaunch(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleMissionLaunchReturnsRunID(t *testing.T) {
	t.Parallel()

	now := metav1.NewTime(time.Now())
	run := &corev1alpha1.LegatorRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "watchman-light-run-1",
			Namespace:         "agents",
			CreationTimestamp: now,
		},
		Spec: corev1alpha1.LegatorRunSpec{AgentRef: "watchman-light"},
	}

	scheme := dashboardTestScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(run).Build()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer apiSrv.Close()

	srv := &Server{
		client:     k8sClient,
		config:     Config{BasePath: "", APIBaseURL: apiSrv.URL},
		log:        logr.Discard(),
		httpClient: apiSrv.Client(),
	}

	form := url.Values{"agent": []string{"watchman-light"}, "intent": []string{"probe cluster"}}
	req := httptest.NewRequest(http.MethodPost, "/cockpit/missions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, &OIDCUser{Subject: "u1", Email: "op@example.com"}))

	rr := httptest.NewRecorder()
	srv.handleMissionLaunch(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "watchman-light-run-1") {
		t.Fatalf("expected run id in response, got: %s", rr.Body.String())
	}
}

func TestGateOutcomeMapping(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"executed":         "allowed",
		"blocked":          "blocked",
		"pending-approval": "pending",
		"approved":         "approved",
		"denied":           "denied",
		"failed":           "blocked",
	}

	for in, want := range cases {
		if got := gateOutcome(in); got != want {
			t.Fatalf("gateOutcome(%q)=%q want=%q", in, got, want)
		}
	}
}
