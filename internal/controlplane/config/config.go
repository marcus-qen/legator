// Package config provides configuration loading for the control plane.
// Configuration sources (in priority order): env vars > config file > defaults.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/marcus-qen/legator/internal/controlplane/oidc"
)

// Config holds all control plane configuration.
type Config struct {
	// Listen address (default ":8080")
	ListenAddr string `json:"listen_addr"`
	// Data directory for SQLite databases (default "/var/lib/legator")
	DataDir string `json:"data_dir"`

	// TLS settings
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`

	// Auth
	AuthEnabled bool `json:"auth_enabled"`

	// OIDC settings (optional)
	OIDC oidc.Config `json:"oidc,omitempty"`

	// Signing key for HMAC (hex-encoded, 64+ chars)
	SigningKey string `json:"signing_key,omitempty"`

	// LLM settings
	LLM LLMConfig `json:"llm,omitempty"`

	// Rate limiting
	RateLimit RateLimitConfig `json:"rate_limit,omitempty"`

	// Log level (debug, info, warn, error)
	LogLevel string `json:"log_level"`

	// External URL for install commands (e.g. https://legator.example.com)
	ExternalURL string `json:"external_url,omitempty"`
}

// LLMConfig configures the LLM provider.
type LLMConfig struct {
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Model    string `json:"model,omitempty"`
}

// RateLimitConfig configures per-key rate limiting.
type RateLimitConfig struct {
	RequestsPerMinute int `json:"requests_per_minute"`
}

// Default returns configuration with sensible defaults.
func Default() Config {
	return Config{
		ListenAddr: ":8080",
		DataDir:    "/var/lib/legator",
		LogLevel:   "info",
		OIDC:       oidc.DefaultConfig(),
		RateLimit: RateLimitConfig{
			RequestsPerMinute: 120,
		},
	}
}

// Load reads configuration from a file, then overlays environment variables.
func Load(path string) (Config, error) {
	cfg := Default()

	// Load from file if it exists
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config: %w", err)
		}
	}

	// Environment variable overrides
	if v := os.Getenv("LEGATOR_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("LEGATOR_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("LEGATOR_TLS_CERT"); v != "" {
		cfg.TLSCert = v
	}
	if v := os.Getenv("LEGATOR_TLS_KEY"); v != "" {
		cfg.TLSKey = v
	}
	if v := os.Getenv("LEGATOR_AUTH"); v != "" {
		cfg.AuthEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_SIGNING_KEY"); v != "" {
		cfg.SigningKey = v
	}
	if v := os.Getenv("LEGATOR_LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := os.Getenv("LEGATOR_LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := os.Getenv("LEGATOR_LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("LEGATOR_LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := os.Getenv("LEGATOR_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("LEGATOR_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimit.RequestsPerMinute = n
		}
	}
	if v := os.Getenv("LEGATOR_EXTERNAL_URL"); v != "" {
		cfg.ExternalURL = v
	}

	cfg.OIDC = oidc.ApplyEnv(cfg.OIDC)

	return cfg, nil
}

// LoadFromEnv loads configuration from environment variables only.
func LoadFromEnv() Config {
	cfg, _ := Load("")
	return cfg
}

// Save writes configuration to a file.
func (c Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0640)
}

// HasTLS returns true if TLS is configured.
func (c Config) HasTLS() bool {
	return c.TLSCert != "" && c.TLSKey != ""
}

// HasLLM returns true if an LLM provider is configured.
func (c Config) HasLLM() bool {
	return c.LLM.Provider != ""
}
