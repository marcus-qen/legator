// Package config provides configuration loading for the control plane.
// Configuration sources (in priority order): env vars > config file > defaults.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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

	// Probe mTLS authentication settings for /ws/probe.
	ProbeMTLS ProbeMTLSConfig `json:"probe_mtls,omitempty"`

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

	// Kubeflow adapter settings (optional)
	Kubeflow KubeflowConfig `json:"kubeflow,omitempty"`

	// Grafana adapter settings (optional)
	Grafana GrafanaConfig `json:"grafana,omitempty"`

	// Scheduled jobs defaults
	Jobs JobsConfig `json:"jobs,omitempty"`

	// Scoped token broker settings for runner operations.
	TokenBroker TokenBrokerConfig `json:"token_broker,omitempty"`

	// Provider proxy spend limits for runner mediated LLM calls.
	ProviderProxy ProviderProxyConfig `json:"provider_proxy,omitempty"`

	// Workspace isolation controls workspace-scoped authorization gates across
	// runner, approvals, command streams, and audit APIs.
	WorkspaceIsolation WorkspaceIsolationConfig `json:"workspace_isolation,omitempty"`

	// Approval controls quorum behavior for high-risk mutation approvals.
	Approval ApprovalConfig `json:"approval,omitempty"`

	// Audit controls optional signed hash-chain settings.
	Audit AuditConfig `json:"audit,omitempty"`

	// Sandbox controls the sandbox session lifecycle API.
	Sandbox SandboxConfig `json:"sandbox,omitempty"`

	// Log level (debug, info, warn, error)
	LogLevel string `json:"log_level"`

	// Audit retention window (e.g. "30d", "90d"). Empty disables auto-purge.
	AuditRetention string `json:"audit_retention,omitempty"`

	// External URL for install commands (e.g. https://legator.example.com)
	ExternalURL string `json:"external_url,omitempty"`

	// MCP endpoint enablement
	MCPEnabled bool `json:"mcp_enabled"`

	// SandboxEnforcement blocks mutation-capable host-direct execution unless
	// explicit breakglass confirmation is supplied.
	SandboxEnforcement bool `json:"sandbox_enforcement"`
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

// KubeflowConfig controls the Kubeflow adapter integration.
type KubeflowConfig struct {
	Enabled        bool   `json:"enabled"`
	Namespace      string `json:"namespace,omitempty"`
	Kubeconfig     string `json:"kubeconfig,omitempty"`
	Context        string `json:"context,omitempty"`
	CLIPath        string `json:"cli_path,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
	ActionsEnabled bool   `json:"actions_enabled,omitempty"`
}

// GrafanaConfig controls the Grafana read-only capacity adapter.
type GrafanaConfig struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url,omitempty"`
	APIToken       string `json:"api_token,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
	DashboardLimit int    `json:"dashboard_limit,omitempty"`
	TLSSkipVerify  bool   `json:"tls_skip_verify,omitempty"`
	OrgID          int    `json:"org_id,omitempty"`
}

// JobsConfig controls scheduler defaults for retry behavior and async worker bounds.
type JobsConfig struct {
	RetryMaxAttempts    int     `json:"retry_max_attempts,omitempty"`
	RetryInitialBackoff string  `json:"retry_initial_backoff,omitempty"`
	RetryMultiplier     float64 `json:"retry_multiplier,omitempty"`
	RetryMaxBackoff     string  `json:"retry_max_backoff,omitempty"`

	AsyncMaxInFlight         int    `json:"async_max_in_flight,omitempty"`
	AsyncPerProbeMaxInFlight int    `json:"async_per_probe_max_in_flight,omitempty"`
	AsyncMaxQueueDepth       int    `json:"async_max_queue_depth,omitempty"`
	AsyncPollInterval        string `json:"async_poll_interval,omitempty"`
	AsyncFetchBatchSize      int    `json:"async_fetch_batch_size,omitempty"`

	StreamMaxEventsPerRequest int    `json:"stream_max_events_per_request,omitempty"`
	StreamMaxEventsTotal      int    `json:"stream_max_events_total,omitempty"`
	StreamRetention           string `json:"stream_retention,omitempty"`

	ApprovalTimeoutSeconds      int    `json:"approval_timeout_seconds,omitempty"`
	ApprovalTimeoutBehavior     string `json:"approval_timeout_behavior,omitempty"`
	RunTokenTTL                 string `json:"run_token_ttl,omitempty"`
	RunnerSandboxRuntimeCommand string `json:"runner_sandbox_runtime_command,omitempty"`
	RunnerSandboxImage          string `json:"runner_sandbox_image,omitempty"`
	RunnerSandboxTimeout        string `json:"runner_sandbox_timeout,omitempty"`
}

// TokenBrokerConfig controls scoped token defaults and scope bounds.
type TokenBrokerConfig struct {
	DefaultTTL string `json:"default_ttl,omitempty"`
	MaxScope   int    `json:"max_scope,omitempty"`
}

type ProviderProxyConfig struct {
	MaxTokensPerRun int     `json:"max_tokens_per_run,omitempty"`
	MaxCostPerRun   float64 `json:"max_cost_per_run,omitempty"`
}

type WorkspaceIsolationConfig struct {
	Enabled bool `json:"enabled"`
}

type ApprovalConfig struct {
	TwoPersonMode bool `json:"two_person_mode,omitempty"`
}

type AuditConfig struct {
	ChainMode bool   `json:"chain_mode,omitempty"`
	ChainKey  string `json:"chain_key,omitempty"`
}

// SandboxConfig controls the sandbox session lifecycle API.
type SandboxConfig struct {
	// AllowedRuntimes restricts which runtime_class values may be requested.
	// An empty or nil slice means all runtimes are allowed.
	AllowedRuntimes []string `json:"allowed_runtimes,omitempty"`

	// MaxConcurrent caps the number of non-terminal sandbox sessions globally.
	// Zero means unlimited.
	MaxConcurrent int `json:"max_concurrent,omitempty"`

	// DBPath overrides the default sandbox SQLite database path
	// (default: <data_dir>/sandbox.db).
	DBPath string `json:"db_path,omitempty"`
}

// ProbeMTLSConfig controls optional mTLS auth for probe websocket sessions.
// Mode values:
//   - off      : legacy API key authentication only (default)
//   - optional : accept mTLS certificate OR API key
//   - required : require mTLS certificate (no API key fallback)
type ProbeMTLSConfig struct {
	Mode string `json:"mode,omitempty"`

	// Client CA used to verify probe certificates.
	ClientCAPath string `json:"client_ca_path,omitempty"`
	ClientCAPEM  string `json:"client_ca_pem,omitempty"`

	// Optional issuing CA material for helper endpoint certificate issuance.
	IssuerCertPath string `json:"issuer_cert_path,omitempty"`
	IssuerKeyPath  string `json:"issuer_key_path,omitempty"`
	IssuerCertPEM  string `json:"issuer_cert_pem,omitempty"`
	IssuerKeyPEM   string `json:"issuer_key_pem,omitempty"`

	// Default TTL used by helper issuance endpoint.
	IssueTTL string `json:"issue_ttl,omitempty"`
}

func (k KubeflowConfig) NamespaceOrDefault() string {
	if namespace := strings.TrimSpace(k.Namespace); namespace != "" {
		return namespace
	}
	return "kubeflow"
}

func (k KubeflowConfig) TimeoutDuration() time.Duration {
	raw := strings.TrimSpace(k.Timeout)
	if raw == "" {
		return 15 * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 15 * time.Second
	}
	return d
}

func (g GrafanaConfig) TimeoutDuration() time.Duration {
	raw := strings.TrimSpace(g.Timeout)
	if raw == "" {
		return 10 * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 10 * time.Second
	}
	return d
}

func (g GrafanaConfig) DashboardLimitOrDefault() int {
	if g.DashboardLimit <= 0 {
		return 10
	}
	if g.DashboardLimit > 100 {
		return 100
	}
	return g.DashboardLimit
}

func (j JobsConfig) AsyncPollIntervalDuration() time.Duration {
	raw := strings.TrimSpace(j.AsyncPollInterval)
	if raw == "" {
		return 200 * time.Millisecond
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 200 * time.Millisecond
	}
	return d
}

func (j JobsConfig) ApprovalTimeoutBehaviorOrDefault() string {
	switch strings.TrimSpace(j.ApprovalTimeoutBehavior) {
	case "cancel", "reads_only", "escalate":
		return strings.TrimSpace(j.ApprovalTimeoutBehavior)
	default:
		return "cancel"
	}
}

func (j JobsConfig) ApprovalTimeoutDuration() time.Duration {
	if j.ApprovalTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(j.ApprovalTimeoutSeconds) * time.Second
}

func (j JobsConfig) StreamRetentionDuration() time.Duration {
	raw := strings.TrimSpace(j.StreamRetention)
	if raw == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 24 * time.Hour
	}
	return d
}

func (j JobsConfig) RunTokenTTLDuration() time.Duration {
	raw := strings.TrimSpace(j.RunTokenTTL)
	if raw == "" {
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 2 * time.Minute
	}
	return d
}

func (j JobsConfig) RunnerSandboxTimeoutDuration() time.Duration {
	raw := strings.TrimSpace(j.RunnerSandboxTimeout)
	if raw == "" {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 10 * time.Minute
	}
	return d
}

func (p ProbeMTLSConfig) ModeOrDefault() string {
	switch strings.ToLower(strings.TrimSpace(p.Mode)) {
	case "optional", "required":
		return strings.ToLower(strings.TrimSpace(p.Mode))
	default:
		return "off"
	}
}

func (p ProbeMTLSConfig) Enabled() bool {
	return p.ModeOrDefault() != "off"
}

func (p ProbeMTLSConfig) IssueTTLDuration() time.Duration {
	raw := strings.TrimSpace(p.IssueTTL)
	if raw == "" {
		return 30 * 24 * time.Hour
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 30 * 24 * time.Hour
	}
	if d > 365*24*time.Hour {
		return 365 * 24 * time.Hour
	}
	return d
}

func (t TokenBrokerConfig) DefaultTTLDuration(fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(t.DefaultTTL)
	if raw == "" {
		if fallback > 0 {
			return fallback
		}
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		if fallback > 0 {
			return fallback
		}
		return 2 * time.Minute
	}
	return d
}

func (t TokenBrokerConfig) MaxScopeOrDefault() int {
	if t.MaxScope <= 0 {
		return 8
	}
	if t.MaxScope > 64 {
		return 64
	}
	return t.MaxScope
}

func (c Config) TokenBrokerDefaultTTLDuration() time.Duration {
	return c.TokenBroker.DefaultTTLDuration(c.Jobs.RunTokenTTLDuration())
}

// Default returns configuration with sensible defaults.
func Default() Config {
	return Config{
		ListenAddr:         ":8080",
		DataDir:            "/var/lib/legator",
		LogLevel:           "info",
		OIDC:               oidc.DefaultConfig(),
		MCPEnabled:         true,
		SandboxEnforcement: true,
		RateLimit: RateLimitConfig{
			RequestsPerMinute: 120,
		},
		Kubeflow: KubeflowConfig{
			Enabled:   false,
			Namespace: "kubeflow",
			CLIPath:   "kubectl",
			Timeout:   "15s",
		},
		Grafana: GrafanaConfig{
			Enabled:        false,
			Timeout:        "10s",
			DashboardLimit: 10,
		},
		Jobs: JobsConfig{
			AsyncMaxInFlight:            8,
			AsyncMaxQueueDepth:          500,
			AsyncPollInterval:           "200ms",
			AsyncFetchBatchSize:         64,
			StreamMaxEventsPerRequest:   2000,
			StreamMaxEventsTotal:        100000,
			StreamRetention:             "24h",
			ApprovalTimeoutSeconds:      900,
			ApprovalTimeoutBehavior:     "cancel",
			RunTokenTTL:                 "2m",
			RunnerSandboxRuntimeCommand: "podman",
			RunnerSandboxImage:          "docker.io/library/alpine:3.20",
			RunnerSandboxTimeout:        "10m",
		},
		TokenBroker: TokenBrokerConfig{
			MaxScope: 8,
		},
		ProviderProxy: ProviderProxyConfig{
			MaxTokensPerRun: 0,
			MaxCostPerRun:   0,
		},
		WorkspaceIsolation: WorkspaceIsolationConfig{
			Enabled: false,
		},
		Approval: ApprovalConfig{
			TwoPersonMode: false,
		},
		Audit: AuditConfig{
			ChainMode: false,
		},
		ProbeMTLS: ProbeMTLSConfig{
			Mode:     "off",
			IssueTTL: "720h",
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
	if v := os.Getenv("LEGATOR_PROBE_MTLS_MODE"); v != "" {
		cfg.ProbeMTLS.Mode = v
	}
	if v := os.Getenv("LEGATOR_PROBE_MTLS_CLIENT_CA_PATH"); v != "" {
		cfg.ProbeMTLS.ClientCAPath = v
	}
	if v := os.Getenv("LEGATOR_PROBE_MTLS_CLIENT_CA_PEM"); v != "" {
		cfg.ProbeMTLS.ClientCAPEM = v
	}
	if v := os.Getenv("LEGATOR_PROBE_MTLS_ISSUER_CERT_PATH"); v != "" {
		cfg.ProbeMTLS.IssuerCertPath = v
	}
	if v := os.Getenv("LEGATOR_PROBE_MTLS_ISSUER_KEY_PATH"); v != "" {
		cfg.ProbeMTLS.IssuerKeyPath = v
	}
	if v := os.Getenv("LEGATOR_PROBE_MTLS_ISSUER_CERT_PEM"); v != "" {
		cfg.ProbeMTLS.IssuerCertPEM = v
	}
	if v := os.Getenv("LEGATOR_PROBE_MTLS_ISSUER_KEY_PEM"); v != "" {
		cfg.ProbeMTLS.IssuerKeyPEM = v
	}
	if v := os.Getenv("LEGATOR_PROBE_MTLS_ISSUE_TTL"); v != "" {
		cfg.ProbeMTLS.IssueTTL = v
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
	if v := os.Getenv("LEGATOR_AUDIT_RETENTION"); v != "" {
		cfg.AuditRetention = v
	}
	if v := os.Getenv("LEGATOR_AUDIT_CHAIN_MODE"); v != "" {
		cfg.Audit.ChainMode = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_AUDIT_CHAIN_KEY"); v != "" {
		cfg.Audit.ChainKey = v
	}
	if v := os.Getenv("LEGATOR_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimit.RequestsPerMinute = n
		}
	}
	if v := os.Getenv("LEGATOR_EXTERNAL_URL"); v != "" {
		cfg.ExternalURL = v
	}
	if v := os.Getenv("LEGATOR_KUBEFLOW_ENABLED"); v != "" {
		cfg.Kubeflow.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_KUBEFLOW_NAMESPACE"); v != "" {
		cfg.Kubeflow.Namespace = v
	}
	if v := os.Getenv("LEGATOR_KUBEFLOW_KUBECONFIG"); v != "" {
		cfg.Kubeflow.Kubeconfig = v
	}
	if v := os.Getenv("LEGATOR_KUBEFLOW_CONTEXT"); v != "" {
		cfg.Kubeflow.Context = v
	}
	if v := os.Getenv("LEGATOR_KUBEFLOW_CLI_PATH"); v != "" {
		cfg.Kubeflow.CLIPath = v
	}
	if v := os.Getenv("LEGATOR_KUBEFLOW_TIMEOUT"); v != "" {
		cfg.Kubeflow.Timeout = v
	}
	if v := os.Getenv("LEGATOR_KUBEFLOW_ACTIONS_ENABLED"); v != "" {
		cfg.Kubeflow.ActionsEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_GRAFANA_ENABLED"); v != "" {
		cfg.Grafana.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_GRAFANA_BASE_URL"); v != "" {
		cfg.Grafana.BaseURL = v
	}
	if v := os.Getenv("LEGATOR_GRAFANA_API_TOKEN"); v != "" {
		cfg.Grafana.APIToken = v
	}
	if v := os.Getenv("LEGATOR_GRAFANA_TIMEOUT"); v != "" {
		cfg.Grafana.Timeout = v
	}
	if v := os.Getenv("LEGATOR_GRAFANA_DASHBOARD_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Grafana.DashboardLimit = n
		}
	}
	if v := os.Getenv("LEGATOR_GRAFANA_TLS_SKIP_VERIFY"); v != "" {
		cfg.Grafana.TLSSkipVerify = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_GRAFANA_ORG_ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Grafana.OrgID = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_RETRY_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.RetryMaxAttempts = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_RETRY_INITIAL_BACKOFF"); v != "" {
		cfg.Jobs.RetryInitialBackoff = v
	}
	if v := os.Getenv("LEGATOR_JOBS_RETRY_MULTIPLIER"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Jobs.RetryMultiplier = f
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_RETRY_MAX_BACKOFF"); v != "" {
		cfg.Jobs.RetryMaxBackoff = v
	}
	if v := os.Getenv("LEGATOR_JOBS_ASYNC_MAX_IN_FLIGHT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.AsyncMaxInFlight = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_ASYNC_PER_PROBE_MAX_IN_FLIGHT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.AsyncPerProbeMaxInFlight = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_ASYNC_MAX_QUEUE_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.AsyncMaxQueueDepth = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_ASYNC_POLL_INTERVAL"); v != "" {
		cfg.Jobs.AsyncPollInterval = v
	}
	if v := os.Getenv("LEGATOR_JOBS_ASYNC_FETCH_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.AsyncFetchBatchSize = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_STREAM_MAX_EVENTS_PER_REQUEST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.StreamMaxEventsPerRequest = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_STREAM_MAX_EVENTS_TOTAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.StreamMaxEventsTotal = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_STREAM_RETENTION"); v != "" {
		cfg.Jobs.StreamRetention = v
	}
	if v := os.Getenv("LEGATOR_JOBS_APPROVAL_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Jobs.ApprovalTimeoutSeconds = n
		}
	}
	if v := os.Getenv("LEGATOR_JOBS_APPROVAL_TIMEOUT_BEHAVIOR"); v != "" {
		cfg.Jobs.ApprovalTimeoutBehavior = v
	}
	if v := os.Getenv("LEGATOR_JOBS_RUN_TOKEN_TTL"); v != "" {
		cfg.Jobs.RunTokenTTL = v
	}
	if v := os.Getenv("LEGATOR_JOBS_RUNNER_SANDBOX_RUNTIME_COMMAND"); v != "" {
		cfg.Jobs.RunnerSandboxRuntimeCommand = v
	}
	if v := os.Getenv("LEGATOR_JOBS_RUNNER_SANDBOX_IMAGE"); v != "" {
		cfg.Jobs.RunnerSandboxImage = v
	}
	if v := os.Getenv("LEGATOR_JOBS_RUNNER_SANDBOX_TIMEOUT"); v != "" {
		cfg.Jobs.RunnerSandboxTimeout = v
	}
	if v := os.Getenv("LEGATOR_TOKEN_BROKER_DEFAULT_TTL"); v != "" {
		cfg.TokenBroker.DefaultTTL = v
	}
	if v := os.Getenv("LEGATOR_TOKEN_BROKER_MAX_SCOPE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TokenBroker.MaxScope = n
		}
	}
	if v := os.Getenv("LEGATOR_PROVIDER_PROXY_MAX_TOKENS_PER_RUN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.ProviderProxy.MaxTokensPerRun = n
		}
	}
	if v := os.Getenv("LEGATOR_PROVIDER_PROXY_MAX_COST_PER_RUN"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.ProviderProxy.MaxCostPerRun = f
		}
	}
	if v := os.Getenv("LEGATOR_MCP_ENABLED"); v != "" {
		cfg.MCPEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_SANDBOX_ENFORCEMENT"); v != "" {
		cfg.SandboxEnforcement = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_WORKSPACE_ISOLATION_ENABLED"); v != "" {
		cfg.WorkspaceIsolation.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("LEGATOR_APPROVAL_TWO_PERSON_MODE"); v != "" {
		cfg.Approval.TwoPersonMode = v == "true" || v == "1"
	}

	if v := os.Getenv("LEGATOR_SANDBOX_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Sandbox.MaxConcurrent = n
		}
	}
	if v := os.Getenv("LEGATOR_SANDBOX_ALLOWED_RUNTIMES"); v != "" {
		parts := strings.Split(v, ",")
		cfg.Sandbox.AllowedRuntimes = make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				cfg.Sandbox.AllowedRuntimes = append(cfg.Sandbox.AllowedRuntimes, t)
			}
		}
	}
	if v := os.Getenv("LEGATOR_SANDBOX_DB_PATH"); v != "" {
		cfg.Sandbox.DBPath = v
	}

	cfg.OIDC = oidc.ApplyEnv(cfg.OIDC)
	cfg.ProbeMTLS.Mode = cfg.ProbeMTLS.ModeOrDefault()

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
