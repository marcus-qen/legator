package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus-qen/legator/internal/probe/agent"
	"go.uber.org/zap"
)

func TestAutoInitConfigFromEnvRegistersWhenConfigMissing(t *testing.T) {
	configDir := t.TempDir()

	var registerCalled bool
	var tagsCalled bool
	var gotHostname string
	var gotTags []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/register":
			registerCalled = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read register body: %v", err)
			}
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode register body: %v", err)
			}
			if hostname, ok := req["hostname"].(string); ok {
				gotHostname = hostname
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"probe_id":  "probe-auto-1",
				"api_key":   "lgk_auto_probe",
				"policy_id": "default-observe",
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/probes/probe-auto-1/tags":
			tagsCalled = true
			var payload struct {
				Tags []string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode tags body: %v", err)
			}
			gotTags = payload.Tags
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": payload.Tags})
		default:
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
	if !tagsCalled {
		t.Fatal("expected tags call")
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
