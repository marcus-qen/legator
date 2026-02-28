package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()
	if cfg.ListenAddr != ":8080" {
		t.Errorf("expected :8080, got %s", cfg.ListenAddr)
	}
	if cfg.DataDir != "/var/lib/legator" {
		t.Errorf("expected /var/lib/legator, got %s", cfg.DataDir)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected info, got %s", cfg.LogLevel)
	}
	if cfg.OIDC.DefaultRole != "viewer" {
		t.Errorf("expected OIDC default role viewer, got %s", cfg.OIDC.DefaultRole)
	}
	if !cfg.MCPEnabled {
		t.Error("expected MCP enabled by default")
	}
	if cfg.Kubeflow.Enabled {
		t.Error("expected kubeflow disabled by default")
	}
	if cfg.Kubeflow.Namespace != "kubeflow" {
		t.Errorf("expected kubeflow namespace default kubeflow, got %s", cfg.Kubeflow.Namespace)
	}
	if cfg.Kubeflow.TimeoutDuration().String() != (15 * time.Second).String() {
		t.Errorf("expected kubeflow timeout default 15s, got %s", cfg.Kubeflow.TimeoutDuration())
	}
	if cfg.Grafana.Enabled {
		t.Error("expected grafana disabled by default")
	}
	if cfg.Grafana.TimeoutDuration() != 10*time.Second {
		t.Errorf("expected grafana timeout default 10s, got %s", cfg.Grafana.TimeoutDuration())
	}
	if cfg.Grafana.DashboardLimitOrDefault() != 10 {
		t.Errorf("expected grafana dashboard limit default 10, got %d", cfg.Grafana.DashboardLimitOrDefault())
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{
		"listen_addr": ":9090",
		"data_dir": "/tmp/test",
		"auth_enabled": true,
		"audit_retention": "90d",
		"mcp_enabled": false,
		"kubeflow": {
			"enabled": true,
			"namespace": "ml-platform",
			"kubeconfig": "/etc/kubeconfig",
			"context": "lab",
			"cli_path": "kubectl",
			"timeout": "20s",
			"actions_enabled": true
		},
		"grafana": {
			"enabled": true,
			"base_url": "https://grafana.lab.local",
			"api_token": "token",
			"timeout": "12s",
			"dashboard_limit": 25,
			"tls_skip_verify": true,
			"org_id": 2
		},
		"oidc": {
			"enabled": true,
			"provider_url": "https://id.example.com/realms/dev",
			"client_id": "legator"
		},
		"llm": {
			"provider": "openai",
			"model": "gpt-4"
		}
	}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Errorf("expected :9090, got %s", cfg.ListenAddr)
	}
	if cfg.DataDir != "/tmp/test" {
		t.Errorf("expected /tmp/test, got %s", cfg.DataDir)
	}
	if !cfg.AuthEnabled {
		t.Error("expected auth enabled")
	}
	if cfg.AuditRetention != "90d" {
		t.Errorf("expected audit retention 90d, got %s", cfg.AuditRetention)
	}
	if cfg.LLM.Provider != "openai" {
		t.Errorf("expected openai, got %s", cfg.LLM.Provider)
	}
	if cfg.MCPEnabled {
		t.Error("expected mcp_enabled=false from file")
	}
	if !cfg.Kubeflow.Enabled {
		t.Fatal("expected kubeflow enabled from file")
	}
	if cfg.Kubeflow.Namespace != "ml-platform" {
		t.Fatalf("unexpected kubeflow namespace: %s", cfg.Kubeflow.Namespace)
	}
	if cfg.Kubeflow.Kubeconfig != "/etc/kubeconfig" {
		t.Fatalf("unexpected kubeflow kubeconfig: %s", cfg.Kubeflow.Kubeconfig)
	}
	if cfg.Kubeflow.Context != "lab" {
		t.Fatalf("unexpected kubeflow context: %s", cfg.Kubeflow.Context)
	}
	if cfg.Kubeflow.TimeoutDuration() != 20*time.Second {
		t.Fatalf("unexpected kubeflow timeout: %s", cfg.Kubeflow.TimeoutDuration())
	}
	if !cfg.Kubeflow.ActionsEnabled {
		t.Fatal("expected kubeflow actions enabled from file")
	}
	if !cfg.Grafana.Enabled {
		t.Fatal("expected grafana enabled from file")
	}
	if cfg.Grafana.BaseURL != "https://grafana.lab.local" {
		t.Fatalf("unexpected grafana base url: %s", cfg.Grafana.BaseURL)
	}
	if cfg.Grafana.APIToken != "token" {
		t.Fatalf("unexpected grafana api token: %s", cfg.Grafana.APIToken)
	}
	if cfg.Grafana.TimeoutDuration() != 12*time.Second {
		t.Fatalf("unexpected grafana timeout: %s", cfg.Grafana.TimeoutDuration())
	}
	if cfg.Grafana.DashboardLimitOrDefault() != 25 {
		t.Fatalf("unexpected grafana dashboard limit: %d", cfg.Grafana.DashboardLimitOrDefault())
	}
	if !cfg.Grafana.TLSSkipVerify {
		t.Fatal("expected grafana tls skip verify from file")
	}
	if cfg.Grafana.OrgID != 2 {
		t.Fatalf("unexpected grafana org id: %d", cfg.Grafana.OrgID)
	}
	if !cfg.OIDC.Enabled {
		t.Fatal("expected oidc enabled")
	}
	if cfg.OIDC.ProviderURL != "https://id.example.com/realms/dev" {
		t.Fatalf("unexpected oidc provider url: %s", cfg.OIDC.ProviderURL)
	}
	if cfg.OIDC.ClientID != "legator" {
		t.Fatalf("unexpected oidc client id: %s", cfg.OIDC.ClientID)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"listen_addr": ":9090"}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("LEGATOR_LISTEN_ADDR", ":7070")
	t.Setenv("LEGATOR_AUTH", "true")
	t.Setenv("LEGATOR_MCP_ENABLED", "0")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != ":7070" {
		t.Errorf("env should override file: got %s", cfg.ListenAddr)
	}
	if !cfg.AuthEnabled {
		t.Error("env LEGATOR_AUTH=true should enable auth")
	}
	if cfg.MCPEnabled {
		t.Error("env LEGATOR_MCP_ENABLED=0 should disable MCP")
	}
}

func TestLoadFromEnvOnly(t *testing.T) {
	t.Setenv("LEGATOR_DATA_DIR", "/tmp/env-test")
	t.Setenv("LEGATOR_LOG_LEVEL", "debug")
	t.Setenv("LEGATOR_AUDIT_RETENTION", "30d")
	t.Setenv("LEGATOR_MCP_ENABLED", "false")
	t.Setenv("LEGATOR_KUBEFLOW_ENABLED", "1")
	t.Setenv("LEGATOR_KUBEFLOW_NAMESPACE", "kubeflow-user")
	t.Setenv("LEGATOR_KUBEFLOW_TIMEOUT", "45s")
	t.Setenv("LEGATOR_KUBEFLOW_ACTIONS_ENABLED", "true")
	t.Setenv("LEGATOR_GRAFANA_ENABLED", "true")
	t.Setenv("LEGATOR_GRAFANA_BASE_URL", "https://grafana.example.com")
	t.Setenv("LEGATOR_GRAFANA_API_TOKEN", "env-token")
	t.Setenv("LEGATOR_GRAFANA_TIMEOUT", "18s")
	t.Setenv("LEGATOR_GRAFANA_DASHBOARD_LIMIT", "40")
	t.Setenv("LEGATOR_GRAFANA_TLS_SKIP_VERIFY", "1")
	t.Setenv("LEGATOR_GRAFANA_ORG_ID", "9")

	cfg := LoadFromEnv()
	if cfg.DataDir != "/tmp/env-test" {
		t.Errorf("expected /tmp/env-test, got %s", cfg.DataDir)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected debug, got %s", cfg.LogLevel)
	}
	if cfg.AuditRetention != "30d" {
		t.Errorf("expected audit retention 30d, got %s", cfg.AuditRetention)
	}
	if cfg.MCPEnabled {
		t.Error("expected MCP disabled from env")
	}
	if !cfg.Kubeflow.Enabled {
		t.Error("expected kubeflow enabled from env")
	}
	if cfg.Kubeflow.Namespace != "kubeflow-user" {
		t.Errorf("expected kubeflow namespace kubeflow-user, got %s", cfg.Kubeflow.Namespace)
	}
	if cfg.Kubeflow.TimeoutDuration() != 45*time.Second {
		t.Errorf("expected kubeflow timeout 45s, got %s", cfg.Kubeflow.TimeoutDuration())
	}
	if !cfg.Kubeflow.ActionsEnabled {
		t.Error("expected kubeflow actions enabled from env")
	}
	if !cfg.Grafana.Enabled {
		t.Error("expected grafana enabled from env")
	}
	if cfg.Grafana.BaseURL != "https://grafana.example.com" {
		t.Errorf("expected grafana base URL override, got %s", cfg.Grafana.BaseURL)
	}
	if cfg.Grafana.APIToken != "env-token" {
		t.Errorf("expected grafana api token override, got %s", cfg.Grafana.APIToken)
	}
	if cfg.Grafana.TimeoutDuration() != 18*time.Second {
		t.Errorf("expected grafana timeout 18s, got %s", cfg.Grafana.TimeoutDuration())
	}
	if cfg.Grafana.DashboardLimitOrDefault() != 40 {
		t.Errorf("expected grafana dashboard limit 40, got %d", cfg.Grafana.DashboardLimitOrDefault())
	}
	if !cfg.Grafana.TLSSkipVerify {
		t.Error("expected grafana tls skip verify from env")
	}
	if cfg.Grafana.OrgID != 9 {
		t.Errorf("expected grafana org id 9, got %d", cfg.Grafana.OrgID)
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	cfg := Default()
	cfg.ListenAddr = ":3000"
	cfg.LLM.Provider = "anthropic"

	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.ListenAddr != ":3000" {
		t.Errorf("expected :3000, got %s", loaded.ListenAddr)
	}
	if loaded.LLM.Provider != "anthropic" {
		t.Errorf("expected anthropic, got %s", loaded.LLM.Provider)
	}
}

func TestHasTLS(t *testing.T) {
	cfg := Default()
	if cfg.HasTLS() {
		t.Error("default should not have TLS")
	}
	cfg.TLSCert = "/path/cert.pem"
	cfg.TLSKey = "/path/key.pem"
	if !cfg.HasTLS() {
		t.Error("should have TLS with both cert and key")
	}
}

func TestJobsRetryEnvOverrides(t *testing.T) {
	t.Setenv("LEGATOR_JOBS_RETRY_MAX_ATTEMPTS", "4")
	t.Setenv("LEGATOR_JOBS_RETRY_INITIAL_BACKOFF", "3s")
	t.Setenv("LEGATOR_JOBS_RETRY_MULTIPLIER", "2.5")
	t.Setenv("LEGATOR_JOBS_RETRY_MAX_BACKOFF", "30s")

	cfg := LoadFromEnv()
	if cfg.Jobs.RetryMaxAttempts != 4 {
		t.Fatalf("expected retry max attempts 4, got %d", cfg.Jobs.RetryMaxAttempts)
	}
	if cfg.Jobs.RetryInitialBackoff != "3s" {
		t.Fatalf("expected initial backoff 3s, got %s", cfg.Jobs.RetryInitialBackoff)
	}
	if cfg.Jobs.RetryMultiplier != 2.5 {
		t.Fatalf("expected retry multiplier 2.5, got %v", cfg.Jobs.RetryMultiplier)
	}
	if cfg.Jobs.RetryMaxBackoff != "30s" {
		t.Fatalf("expected max backoff 30s, got %s", cfg.Jobs.RetryMaxBackoff)
	}
}

func TestOIDCEnvOverrides(t *testing.T) {
	t.Setenv("LEGATOR_OIDC_ENABLED", "true")
	t.Setenv("LEGATOR_OIDC_PROVIDER_URL", "https://keycloak.example.com/realms/dev")
	t.Setenv("LEGATOR_OIDC_CLIENT_ID", "legator")
	t.Setenv("LEGATOR_OIDC_CLIENT_SECRET", "shh")
	t.Setenv("LEGATOR_OIDC_REDIRECT_URL", "https://legator.example.com/auth/oidc/callback")
	t.Setenv("LEGATOR_OIDC_SCOPES", "openid,email,profile,groups")
	t.Setenv("LEGATOR_OIDC_ROLE_CLAIM", "groups")
	t.Setenv("LEGATOR_OIDC_ROLE_MAPPING", "platform-admins=admin,developers=operator")
	t.Setenv("LEGATOR_OIDC_DEFAULT_ROLE", "viewer")
	t.Setenv("LEGATOR_OIDC_AUTO_CREATE_USERS", "false")
	t.Setenv("LEGATOR_OIDC_PROVIDER_NAME", "Keycloak")

	cfg := LoadFromEnv()
	if !cfg.OIDC.Enabled {
		t.Fatal("expected oidc enabled from env")
	}
	if cfg.OIDC.ProviderURL != "https://keycloak.example.com/realms/dev" {
		t.Fatalf("unexpected provider URL: %s", cfg.OIDC.ProviderURL)
	}
	if cfg.OIDC.ClientID != "legator" || cfg.OIDC.ClientSecret != "shh" {
		t.Fatalf("unexpected client credentials: %#v", cfg.OIDC)
	}
	if cfg.OIDC.RedirectURL != "https://legator.example.com/auth/oidc/callback" {
		t.Fatalf("unexpected redirect URL: %s", cfg.OIDC.RedirectURL)
	}
	if len(cfg.OIDC.Scopes) != 4 {
		t.Fatalf("expected 4 scopes, got %d", len(cfg.OIDC.Scopes))
	}
	if cfg.OIDC.RoleMapping["platform-admins"] != "admin" {
		t.Fatalf("role mapping not loaded: %#v", cfg.OIDC.RoleMapping)
	}
	if cfg.OIDC.AutoCreateUsers {
		t.Fatal("expected auto_create_users=false from env")
	}
	if cfg.OIDC.ProviderName != "Keycloak" {
		t.Fatalf("expected provider name Keycloak, got %s", cfg.OIDC.ProviderName)
	}
}
