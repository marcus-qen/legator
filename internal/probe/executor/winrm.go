// Package executor — WinRM remote execution on Windows hosts (pure-Go, no CGO).
package executor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/masterzen/winrm"
	"go.uber.org/zap"
)

const (
	defaultWinRMPort    = 5985
	defaultWinRMPortTLS = 5986
	winRMMaxOutputSize  = 1 << 20 // 1 MB per stream
)

// WinRMAuthType selects the authentication mechanism for WinRM connections.
type WinRMAuthType string

const (
	// WinRMAuthBasic uses HTTP Basic authentication (plaintext; use with HTTPS only).
	WinRMAuthBasic WinRMAuthType = "basic"
	// WinRMAuthNTLM uses NTLMv2 authentication (default; works without HTTPS).
	WinRMAuthNTLM WinRMAuthType = "ntlm"
	// WinRMAuthKerberos uses Kerberos / SPNEGO authentication (domain environments).
	WinRMAuthKerberos WinRMAuthType = "kerberos"
)

// WinRMConfig holds connection parameters for a remote Windows host.
type WinRMConfig struct {
	Host     string        `yaml:"host"`
	Port     int           `yaml:"port,omitempty"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Auth     WinRMAuthType `yaml:"auth,omitempty"` // basic | ntlm (default) | kerberos
	HTTPS    bool          `yaml:"https,omitempty"`
	Insecure bool          `yaml:"insecure,omitempty"` // skip TLS certificate verification
	Timeout  time.Duration `yaml:"timeout,omitempty"`

	// Kerberos-specific (required when Auth == "kerberos").
	KrbRealm  string `yaml:"krb_realm,omitempty"`
	KrbConfig string `yaml:"krb_config,omitempty"` // path to krb5.conf
	KrbCCache string `yaml:"krb_ccache,omitempty"` // path to credential cache
	KrbSPN    string `yaml:"krb_spn,omitempty"`    // service principal name override
}

// Validate returns an error if required fields are missing or values are invalid.
func (c *WinRMConfig) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("winrm: host is required")
	}
	if c.User == "" {
		return fmt.Errorf("winrm: user is required")
	}
	if c.Password == "" {
		return fmt.Errorf("winrm: password is required")
	}
	switch c.Auth {
	case WinRMAuthBasic, WinRMAuthNTLM, WinRMAuthKerberos, "":
		// valid (empty defaults to NTLM)
	default:
		return fmt.Errorf("winrm: unknown auth type %q (valid: basic, ntlm, kerberos)", c.Auth)
	}
	if c.Auth == WinRMAuthKerberos && c.KrbRealm == "" {
		return fmt.Errorf("winrm: krb_realm is required for kerberos auth")
	}
	return nil
}

// effectivePort returns the configured port or the WinRM default for the protocol.
func (c *WinRMConfig) effectivePort() int {
	if c.Port != 0 {
		return c.Port
	}
	if c.HTTPS {
		return defaultWinRMPortTLS
	}
	return defaultWinRMPort
}

// effectiveTimeout returns the configured timeout or the 30-second default.
func (c *WinRMConfig) effectiveTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

// PSRunner is the minimal interface satisfied by *winrm.Client.
// Keeping it small allows tests to inject a mock without importing the winrm package.
type PSRunner interface {
	RunPSWithContextWithString(ctx context.Context, command, stdin string) (stdout, stderr string, exitCode int, err error)
}

// WinRMExecutor executes PowerShell scripts on a remote Windows host via WinRM.
// It is safe for concurrent use.
type WinRMExecutor struct {
	cfg    WinRMConfig
	runner PSRunner
	logger *zap.Logger
	mu     sync.Mutex
}

// NewWinRMExecutor creates a WinRMExecutor backed by a live WinRM connection.
// The underlying winrm.Client is created lazily — no network connection is made
// until Execute is called.
func NewWinRMExecutor(cfg WinRMConfig, logger *zap.Logger) (*WinRMExecutor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	runner, err := buildWinRMClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("winrm: build client: %w", err)
	}
	return &WinRMExecutor{cfg: cfg, runner: runner, logger: logger}, nil
}

// newWinRMExecutorWithRunner creates a WinRMExecutor with an injected runner.
// Used only in tests.
func newWinRMExecutorWithRunner(cfg WinRMConfig, runner PSRunner, logger *zap.Logger) *WinRMExecutor {
	return &WinRMExecutor{cfg: cfg, runner: runner, logger: logger}
}

// buildWinRMClient constructs a *winrm.Client from WinRMConfig.
// No network connection is made at this point.
func buildWinRMClient(cfg WinRMConfig) (*winrm.Client, error) {
	endpoint := winrm.NewEndpoint(
		cfg.Host,
		cfg.effectivePort(),
		cfg.HTTPS,
		cfg.Insecure,
		nil, nil, nil,
		cfg.effectiveTimeout(),
	)

	auth := cfg.Auth
	if auth == "" {
		auth = WinRMAuthNTLM
	}

	params := winrm.NewParameters("PT60S", "en-US", 153600)
	switch auth {
	case WinRMAuthNTLM:
		params.TransportDecorator = func() winrm.Transporter {
			return &winrm.ClientNTLM{}
		}
	case WinRMAuthKerberos:
		proto := "http"
		if cfg.HTTPS {
			proto = "https"
		}
		settings := &winrm.Settings{
			WinRMUsername: cfg.User,
			WinRMPassword: cfg.Password,
			WinRMHost:     cfg.Host,
			WinRMPort:     cfg.effectivePort(),
			WinRMProto:    proto,
			WinRMInsecure: cfg.Insecure,
			KrbRealm:      cfg.KrbRealm,
			KrbConfig:     cfg.KrbConfig,
			KrbCCache:     cfg.KrbCCache,
			KrbSpn:        cfg.KrbSPN,
		}
		params.TransportDecorator = func() winrm.Transporter {
			return winrm.NewClientKerberos(settings)
		}
	case WinRMAuthBasic:
		// default HTTP transport uses Basic auth headers
	}

	return winrm.NewClientWithParameters(endpoint, cfg.User, cfg.Password, params)
}

// Execute runs a PowerShell script on the remote host and returns a structured result.
// The result is always non-nil. When a transport error occurs, ExitCode is set to -1
// and the error is also returned.
func (e *WinRMExecutor) Execute(ctx context.Context, script string) (*protocol.CommandResultPayload, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	start := time.Now()
	stdout, stderr, exitCode, err := e.runner.RunPSWithContextWithString(ctx, script, "")
	duration := time.Since(start).Milliseconds()

	if err != nil {
		e.logger.Error("winrm execution error",
			zap.String("host", e.cfg.Host),
			zap.Error(err),
		)
		return &protocol.CommandResultPayload{
			ExitCode: -1,
			Stderr:   err.Error(),
			Duration: duration,
		}, err
	}

	truncated := false
	if len(stdout) > winRMMaxOutputSize {
		stdout = stdout[:winRMMaxOutputSize]
		truncated = true
	}
	if len(stderr) > winRMMaxOutputSize {
		stderr = stderr[:winRMMaxOutputSize]
		truncated = true
	}

	e.logger.Info("winrm powershell executed",
		zap.String("host", e.cfg.Host),
		zap.Int("exit_code", exitCode),
		zap.Int64("duration_ms", duration),
	)

	return &protocol.CommandResultPayload{
		ExitCode:  exitCode,
		Stdout:    stdout,
		Stderr:    stderr,
		Duration:  duration,
		Truncated: truncated,
	}, nil
}

// RunPS is a convenience wrapper used by the inventory scanner.
// It wraps Execute and returns stdout/stderr/exitCode directly.
func (e *WinRMExecutor) RunPS(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error) {
	res, execErr := e.Execute(ctx, script)
	if execErr != nil {
		return "", execErr.Error(), -1, execErr
	}
	return res.Stdout, res.Stderr, res.ExitCode, nil
}

// WinRMPool caches WinRMExecutor instances keyed by host:port:auth to avoid
// re-creating clients for repeated connections to the same target.
type WinRMPool struct {
	mu      sync.Mutex
	clients map[string]*WinRMExecutor
	logger  *zap.Logger
}

// NewWinRMPool creates a new empty executor pool.
func NewWinRMPool(logger *zap.Logger) *WinRMPool {
	return &WinRMPool{
		clients: make(map[string]*WinRMExecutor),
		logger:  logger,
	}
}

// Get returns a cached executor for the target or creates and caches a new one.
func (p *WinRMPool) Get(cfg WinRMConfig) (*WinRMExecutor, error) {
	key := fmt.Sprintf("%s:%d:%s", cfg.Host, cfg.effectivePort(), string(cfg.Auth))
	p.mu.Lock()
	defer p.mu.Unlock()

	if ex, ok := p.clients[key]; ok {
		return ex, nil
	}
	ex, err := NewWinRMExecutor(cfg, p.logger)
	if err != nil {
		return nil, err
	}
	p.clients[key] = ex
	return ex, nil
}

// Evict removes the cached executor for the given target, forcing a fresh client
// on the next Get call (useful after a connection error or credential rotation).
func (p *WinRMPool) Evict(cfg WinRMConfig) {
	key := fmt.Sprintf("%s:%d:%s", cfg.Host, cfg.effectivePort(), string(cfg.Auth))
	p.mu.Lock()
	delete(p.clients, key)
	p.mu.Unlock()
}
