package config

import (
	"os"
	"path/filepath"
	"testing"
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
