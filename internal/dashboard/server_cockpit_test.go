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

func TestFetchCockpitConnectivityViaAPI(t *testing.T) {
	t.Parallel()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cockpit/connectivity" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[{"run":"watchman-light-run-1","agent":"watchman-light","phase":"Running","environment":"lab-env","tunnel":{"status":"active","provider":"headscale","target":"10.0.0.5","leaseTtlSeconds":900,"lastTransitionAt":"2026-02-24T23:00:00Z"},"credential":{"mode":"vault-signed-cert","issuer":"ssh-client-signer role=legator-ssh","ttlSeconds":300,"expiresAt":"2026-02-24T23:05:00Z"}}]}`))
	}))
	defer apiSrv.Close()

	srv := &Server{
		config:     Config{BasePath: "", APIBaseURL: apiSrv.URL},
		log:        logr.Discard(),
		httpClient: apiSrv.Client(),
	}

	rows, err := srv.fetchCockpitConnectivityViaAPI(context.Background(), &OIDCUser{Subject: "u1", Email: "ops@example.com"}, 5)
	if err != nil {
		t.Fatalf("fetchCockpitConnectivityViaAPI: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1", len(rows))
	}
	if rows[0].Tunnel.Status != "active" {
		t.Fatalf("tunnel status = %q, want active", rows[0].Tunnel.Status)
	}
	if rows[0].Credential.Mode != "vault-signed-cert" {
		t.Fatalf("credential mode = %q, want vault-signed-cert", rows[0].Credential.Mode)
	}
	if rows[0].Credential.TTLSeconds != 300 {
		t.Fatalf("credential ttl = %d, want 300", rows[0].Credential.TTLSeconds)
	}
}

func TestConnectivityVisualHelpers(t *testing.T) {
	t.Parallel()

	if got := tunnelStatusClass("active"); got != "status-active" {
		t.Fatalf("tunnelStatusClass(active)=%q", got)
	}
	if got := credentialClass("static-key-legacy"); got != "credential-legacy" {
		t.Fatalf("credentialClass(static-key-legacy)=%q", got)
	}

	now := time.Now()
	if got := ttlRemaining(now.Add(90*time.Second), now); got != "1m" {
		t.Fatalf("ttlRemaining 90s=%q want 1m", got)
	}
	if got := ttlRemaining(now.Add(-1*time.Second), now); got != "expired" {
		t.Fatalf("ttlRemaining expired=%q want expired", got)
	}

	label, class := credentialRisk("vault-signed-cert", "12m")
	if label != "low" || class != "risk-low" {
		t.Fatalf("credentialRisk(vault-signed-cert,12m)=%s/%s", label, class)
	}
	label, class = credentialRisk("otp", "45s")
	if label != "high" || class != "risk-high" {
		t.Fatalf("credentialRisk(otp,45s)=%s/%s", label, class)
	}
	label, class = credentialRisk("static-key-legacy", "—")
	if label != "high" || class != "risk-high" {
		t.Fatalf("credentialRisk(static-key-legacy)=%s/%s", label, class)
	}
}
