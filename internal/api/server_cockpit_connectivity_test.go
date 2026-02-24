package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/api/auth"
	"github.com/marcus-qen/legator/internal/api/rbac"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCockpitConnectivitySnapshotIncludesTunnelAndCredentialMetadata(t *testing.T) {
	now := time.Now().UTC()

	env := &corev1alpha1.LegatorEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "lab-env", Namespace: "agents"},
		Spec: corev1alpha1.LegatorEnvironmentSpec{
			Connectivity: &corev1alpha1.ConnectivitySpec{
				Type: "headscale",
				Headscale: &corev1alpha1.HeadscaleConnectivity{
					ControlServer:    "https://headscale.lab",
					AuthKeySecretRef: "headscale-auth",
				},
			},
			Endpoints: map[string]corev1alpha1.EndpointSpec{
				"host-ssh": {URL: "ssh://10.0.0.5:22"},
				"api":      {URL: "https://10.0.0.10"},
			},
			Credentials: map[string]corev1alpha1.CredentialRef{
				"ssh-cert": {
					Type: "vault-ssh-ca",
					Vault: &corev1alpha1.VaultCredentialSpec{
						Mount: "ssh-client-signer",
						Role:  "legator-ssh",
						TTL:   "7m",
					},
				},
			},
		},
	}

	run := &corev1alpha1.LegatorRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "watchman-light-run-1",
			Namespace:         "agents",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
		},
		Spec: corev1alpha1.LegatorRunSpec{
			AgentRef:       "watchman-light",
			EnvironmentRef: "lab-env",
			Trigger:        corev1alpha1.RunTriggerManual,
		},
		Status: corev1alpha1.LegatorRunStatus{
			Phase:     corev1alpha1.RunPhaseRunning,
			StartTime: &metav1.Time{Time: now.Add(-40 * time.Second)},
			Actions: []corev1alpha1.ActionRecord{
				{Seq: 1, Tool: "ssh.exec", Target: "10.0.0.5", Status: corev1alpha1.ActionStatusExecuted, Timestamp: metav1.NewTime(now.Add(-15 * time.Second))},
			},
		},
	}

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{{
			Name:     "viewer",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
			Role:     rbac.RoleViewer,
		}},
	}, newTestClient(t, env, run), logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cockpit/connectivity?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Runs []struct {
			Run        string `json:"run"`
			Phase      string `json:"phase"`
			Tunnel     struct {
				Status          string `json:"status"`
				Provider        string `json:"provider"`
				ControlServer   string `json:"controlServer"`
				LeaseTTLSeconds int64  `json:"leaseTtlSeconds"`
			} `json:"tunnel"`
			Credential struct {
				Mode       string `json:"mode"`
				Issuer     string `json:"issuer"`
				TTLSeconds int64  `json:"ttlSeconds"`
			} `json:"credential"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Runs) != 1 {
		t.Fatalf("runs len = %d, want 1", len(body.Runs))
	}

	got := body.Runs[0]
	if got.Run != "watchman-light-run-1" {
		t.Fatalf("run = %q, want watchman-light-run-1", got.Run)
	}
	if got.Tunnel.Status != "active" {
		t.Fatalf("tunnel.status = %q, want active", got.Tunnel.Status)
	}
	if got.Tunnel.Provider != "headscale" {
		t.Fatalf("tunnel.provider = %q, want headscale", got.Tunnel.Provider)
	}
	if got.Tunnel.ControlServer != "https://headscale.lab" {
		t.Fatalf("tunnel.controlServer = %q", got.Tunnel.ControlServer)
	}
	if got.Tunnel.LeaseTTLSeconds != 900 {
		t.Fatalf("tunnel.leaseTtlSeconds = %d, want 900", got.Tunnel.LeaseTTLSeconds)
	}
	if got.Credential.Mode != "vault-signed-cert" {
		t.Fatalf("credential.mode = %q, want vault-signed-cert", got.Credential.Mode)
	}
	if got.Credential.TTLSeconds != 420 {
		t.Fatalf("credential.ttlSeconds = %d, want 420", got.Credential.TTLSeconds)
	}
}

func TestCockpitConnectivitySnapshotMapsFailedRunToFailedTunnel(t *testing.T) {
	run := &corev1alpha1.LegatorRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "watchman-deep-run-2",
			Namespace:         "agents",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
		},
		Spec: corev1alpha1.LegatorRunSpec{AgentRef: "watchman-deep", EnvironmentRef: "missing-env", Trigger: corev1alpha1.RunTriggerManual},
		Status: corev1alpha1.LegatorRunStatus{Phase: corev1alpha1.RunPhaseFailed},
	}

	srv := NewServer(ServerConfig{
		OIDC: auth.OIDCConfig{BypassPaths: []string{"/healthz"}},
		Policies: []rbac.UserPolicy{{
			Name:     "viewer",
			Subjects: []rbac.SubjectMatcher{{Claim: "email", Value: "viewer@example.com"}},
			Role:     rbac.RoleViewer,
		}},
	}, newTestClient(t, run), logr.Discard())

	token := makeTestJWT(map[string]any{
		"sub":   "viewer-1",
		"email": "viewer@example.com",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cockpit/connectivity", nil)
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
	runs, _ := body["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs len = %d, want 1", len(runs))
	}
	entry := runs[0].(map[string]any)
	tunnel := entry["tunnel"].(map[string]any)
	if tunnel["status"] != "failed" {
		t.Fatalf("tunnel.status = %v, want failed", tunnel["status"])
	}
	if tunnel["provider"] != "direct" {
		t.Fatalf("tunnel.provider = %v, want direct", tunnel["provider"])
	}
}
