package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.uber.org/zap"
)

type registerRequestCapture struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
}

func TestConfigSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	expected := &Config{
		ServerURL: "https://example.test",
		ProbeID:   "probe-1",
		APIKey:    "api-key",
		PolicyID:  "policy-1",
	}

	if err := expected.Save(dir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got.ConfigDir != dir {
		t.Fatalf("expected ConfigDir %q, got %q", dir, got.ConfigDir)
	}
	if got.ServerURL != expected.ServerURL || got.ProbeID != expected.ProbeID || got.APIKey != expected.APIKey || got.PolicyID != expected.PolicyID {
		t.Fatalf("config mismatch: expected %+v, got %+v", expected, got)
	}
}

func TestConfigSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "probe-config")
	cfg := &Config{ServerURL: "https://example.test"}

	if err := cfg.Save(dir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	cfgPath := ConfigPath(dir)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected config file at %s: %v", cfgPath, err)
	}
}

func TestLoadConfigMissingFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadConfig(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected error loading missing config file")
	}
}

func TestRegisterSendsExpectedRequestAndReturnsConfig(t *testing.T) {
	var got registerRequestCapture

	expectedToken := "token-123"
	hostname, _ := os.Hostname()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %q", r.Method)
		}
		if r.URL.Path != "/api/v1/register" {
			t.Errorf("expected path /api/v1/register, got %q", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"probe_id":  "probe-xyz",
			"api_key":   "api-key-123",
			"policy_id": "policy-321",
		})
	}))
	defer ts.Close()

	cfg, err := Register(context.Background(), ts.URL, expectedToken, zap.NewNop())
	if err != nil {
		t.Fatalf("register returned error: %v", err)
	}

	if got.Token != expectedToken {
		t.Fatalf("expected token %q, got %q", expectedToken, got.Token)
	}
	if got.Hostname != hostname {
		t.Fatalf("expected hostname %q, got %q", hostname, got.Hostname)
	}
	if got.OS != runtime.GOOS {
		t.Fatalf("expected os %q, got %q", runtime.GOOS, got.OS)
	}
	if got.Arch != runtime.GOARCH {
		t.Fatalf("expected arch %q, got %q", runtime.GOARCH, got.Arch)
	}
	if got.Version == "" {
		t.Fatal("expected version to be set")
	}
	if cfg.ServerURL != ts.URL {
		t.Fatalf("expected server URL %q, got %q", ts.URL, cfg.ServerURL)
	}
	if cfg.ProbeID != "probe-xyz" || cfg.APIKey != "api-key-123" || cfg.PolicyID != "policy-321" {
		t.Fatalf("unexpected config returned: %+v", cfg)
	}
}

func TestRegisterWithOptionsUsesHostnameOverrideAndSetsTags(t *testing.T) {
	var gotHostname string
	var gotTags []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/register" {
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
				"probe_id":  "probe-tagged",
				"api_key":   "lgk_probe_key",
				"policy_id": "policy-1",
			})
		} else {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	cfg, err := RegisterWithOptions(context.Background(), ts.URL, "token-123", zap.NewNop(), RegisterOptions{
		HostnameOverride: "k8s-node-1",
		Tags:             []string{"kubernetes", "daemonset", "kubernetes"},
	})
	if err != nil {
		t.Fatalf("register returned error: %v", err)
	}

	if gotHostname != "k8s-node-1" {
		t.Fatalf("expected hostname override, got %q", gotHostname)
	}
	if len(gotTags) != 2 || gotTags[0] != "kubernetes" || gotTags[1] != "daemonset" {
		t.Fatalf("unexpected tags: %#v", gotTags)
	}
	if cfg.ProbeID != "probe-tagged" {
		t.Fatalf("unexpected probe ID: %q", cfg.ProbeID)
	}
}

func TestRegisterReturnsErrorOnBadStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})
	}))
	defer ts.Close()

	if _, err := Register(context.Background(), ts.URL, "bad-token", zap.NewNop()); err == nil {
		t.Fatal("expected register to return error on bad status")
	}
}
