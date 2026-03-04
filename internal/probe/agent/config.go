// Package agent implements the probe's main agent loop and configuration.
package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marcus-qen/legator/internal/protocol"
	"gopkg.in/yaml.v3"
)

var (
	DefaultConfigDir = defaultConfigDir()
	DefaultDataDir   = defaultDataDir()
	DefaultLogDir    = defaultLogDir()
)

// Config holds the probe's persistent configuration.
type Config struct {
	ServerURL  string     `yaml:"server_url"`
	ProbeID    string     `yaml:"probe_id"`
	APIKey     string     `yaml:"api_key"`
	PolicyID   string     `yaml:"policy_id,omitempty"`
	SigningKey string     `yaml:"signing_key,omitempty"` // master signing key
	MTLS       MTLSConfig `yaml:"mtls,omitempty"`

	// Last applied local policy (persisted for restart safety).
	PolicyLevel   protocol.CapabilityLevel `yaml:"policy_level,omitempty"`
	PolicyAllowed []string                 `yaml:"policy_allowed,omitempty"`
	PolicyBlocked []string                 `yaml:"policy_blocked,omitempty"`
	PolicyPaths   []string                 `yaml:"policy_paths,omitempty"`

	PolicyExecutionClassRequired protocol.ExecutionClass   `yaml:"policy_execution_class_required,omitempty"`
	PolicySandboxRequired        bool                      `yaml:"policy_sandbox_required,omitempty"`
	PolicyApprovalMode           protocol.ApprovalMode     `yaml:"policy_approval_mode,omitempty"`
	PolicyBreakglass             protocol.BreakglassPolicy `yaml:"policy_breakglass,omitempty"`
	PolicyMaxRuntimeSec          int                       `yaml:"policy_max_runtime_sec,omitempty"`
	PolicyAllowedScopes          []string                  `yaml:"policy_allowed_scopes,omitempty"`

	// WinRMTargets defines remote Windows hosts managed via WinRM (no probe binary required).
	WinRMTargets []WinRMTargetConfig `yaml:"winrm_targets,omitempty"`

	ConfigDir string `yaml:"-"` // not persisted
}

// MTLSConfig controls optional client-certificate auth when connecting to /ws/probe.
type MTLSConfig struct {
	Enabled        bool   `yaml:"enabled,omitempty"`
	ClientCertPath string `yaml:"client_cert_path,omitempty"`
	ClientKeyPath  string `yaml:"client_key_path,omitempty"`
	ClientCertPEM  string `yaml:"client_cert_pem,omitempty"`
	ClientKeyPEM   string `yaml:"client_key_pem,omitempty"`
	RootCAPath     string `yaml:"root_ca_path,omitempty"`
	RootCAPEM      string `yaml:"root_ca_pem,omitempty"`
}

// ConfigPath returns the full path to the config file.
func ConfigPath(configDir string) string {
	if configDir == "" {
		configDir = DefaultConfigDir
	}
	return filepath.Join(configDir, "config.yaml")
}

// LoadConfig reads the probe config from disk.
func LoadConfig(configDir string) (*Config, error) {
	path := ConfigPath(configDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if configDir == "" {
		configDir = DefaultConfigDir
	}
	cfg.ConfigDir = configDir
	return &cfg, nil
}

// Save writes the config to disk with restrictive permissions.
func (c *Config) Save(configDir string) error {
	if configDir == "" {
		configDir = DefaultConfigDir
	}

	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	path := ConfigPath(configDir)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
