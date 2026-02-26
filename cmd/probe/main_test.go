package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus-qen/legator/internal/probe/agent"
	"go.uber.org/zap"
)

func TestAutoInitConfigFromEnvRegistersWhenConfigMissing(t *testing.T) {
	configDir := t.TempDir()

	var registerCalled bool
	var gotHostname string
	var gotTags []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/register" {
			registerCalled = true
			var req struct {
				Hostname string   `json:"hostname"`
				Tags     []string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode register body: %v", err)
			}
			gotHostname = req.Hostname
			gotTags = req.Tags
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"probe_id":  "probe-auto-1",
				"api_key":   "lgk_auto_probe",
				"policy_id": "default-observe",
			})
		} else {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	t.Setenv("LEGATOR_SERVER_URL", ts.URL)
	t.Setenv("LEGATOR_TOKEN", "prb_env_token")
	t.Setenv("LEGATOR_PROBE_TAGS", "kubernetes,daemonset,worker")
	t.Setenv("NODE_NAME", "worker-a")

	if err := autoInitConfigFromEnv(context.Background(), configDir, zap.NewNop()); err != nil {
		t.Fatalf("auto-init returned error: %v", err)
	}

	if !registerCalled {
		t.Fatal("expected registration call")
	}
	if gotHostname != "worker-a" {
		t.Fatalf("expected hostname override worker-a, got %q", gotHostname)
	}
	if len(gotTags) != 3 {
		t.Fatalf("expected 3 tags, got %d (%v)", len(gotTags), gotTags)
	}

	cfg, err := agent.LoadConfig(configDir)
	if err != nil {
		t.Fatalf("expected saved config: %v", err)
	}
	if cfg.ProbeID != "probe-auto-1" {
		t.Fatalf("expected saved probe ID probe-auto-1, got %q", cfg.ProbeID)
	}
	if cfg.APIKey != "lgk_auto_probe" {
		t.Fatalf("expected saved api key lgk_auto_probe, got %q", cfg.APIKey)
	}
}
