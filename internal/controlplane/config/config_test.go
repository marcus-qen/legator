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
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{
		"listen_addr": ":9090",
		"data_dir": "/tmp/test",
		"auth_enabled": true,
		"llm": {
			"provider": "openai",
			"model": "gpt-4"
		}
	}`), 0644)

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
	if cfg.LLM.Provider != "openai" {
		t.Errorf("expected openai, got %s", cfg.LLM.Provider)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"listen_addr": ":9090"}`), 0644)

	t.Setenv("LEGATOR_LISTEN_ADDR", ":7070")
	t.Setenv("LEGATOR_AUTH", "true")

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
}

func TestLoadFromEnvOnly(t *testing.T) {
	t.Setenv("LEGATOR_DATA_DIR", "/tmp/env-test")
	t.Setenv("LEGATOR_LOG_LEVEL", "debug")

	cfg := LoadFromEnv()
	if cfg.DataDir != "/tmp/env-test" {
		t.Errorf("expected /tmp/env-test, got %s", cfg.DataDir)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected debug, got %s", cfg.LogLevel)
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
