package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Mock PSRunner
// ---------------------------------------------------------------------------

type mockPSResponse struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

type mockPSRunner struct {
	queue []mockPSResponse
	calls []string // scripts called
}

func (m *mockPSRunner) RunPSWithContextWithString(_ context.Context, command, _ string) (string, string, int, error) {
	m.calls = append(m.calls, command)
	if len(m.queue) == 0 {
		return "", "", 0, nil
	}
	r := m.queue[0]
	m.queue = m.queue[1:]
	return r.stdout, r.stderr, r.exitCode, r.err
}

func newMockRunner(responses ...mockPSResponse) *mockPSRunner {
	return &mockPSRunner{queue: responses}
}

func nopLogger() *zap.Logger { l, _ := zap.NewDevelopment(); return l }

func testWinRMCfg() WinRMConfig {
	return WinRMConfig{Host: "192.0.2.1", User: "Administrator", Password: "secret"}
}

// ---------------------------------------------------------------------------
// WinRMConfig.Validate
// ---------------------------------------------------------------------------

func TestWinRMConfigValidate_MissingHost(t *testing.T) {
	cfg := WinRMConfig{User: "u", Password: "p"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("expected host error, got %v", err)
	}
}

func TestWinRMConfigValidate_MissingUser(t *testing.T) {
	cfg := WinRMConfig{Host: "h", Password: "p"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "user") {
		t.Fatalf("expected user error, got %v", err)
	}
}

func TestWinRMConfigValidate_MissingPassword(t *testing.T) {
	cfg := WinRMConfig{Host: "h", User: "u"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected password error, got %v", err)
	}
}

func TestWinRMConfigValidate_UnknownAuth(t *testing.T) {
	cfg := WinRMConfig{Host: "h", User: "u", Password: "p", Auth: "digest"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unknown auth") {
		t.Fatalf("expected unknown auth error, got %v", err)
	}
}

func TestWinRMConfigValidate_KerberosRequiresRealm(t *testing.T) {
	cfg := WinRMConfig{Host: "h", User: "u", Password: "p", Auth: WinRMAuthKerberos}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "krb_realm") {
		t.Fatalf("expected krb_realm error, got %v", err)
	}
}

func TestWinRMConfigValidate_Valid(t *testing.T) {
	cases := []WinRMConfig{
		{Host: "h", User: "u", Password: "p"},
		{Host: "h", User: "u", Password: "p", Auth: WinRMAuthNTLM},
		{Host: "h", User: "u", Password: "p", Auth: WinRMAuthBasic, HTTPS: true},
		{Host: "h", User: "u", Password: "p", Auth: WinRMAuthKerberos, KrbRealm: "CORP.LOCAL"},
	}
	for _, cfg := range cases {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("unexpected error for %+v: %v", cfg, err)
		}
	}
}

func TestWinRMConfigEffectivePort(t *testing.T) {
	cfg1 := WinRMConfig{}
	if got := cfg1.effectivePort(); got != defaultWinRMPort {
		t.Fatalf("expected %d, got %d", defaultWinRMPort, got)
	}
	cfg2 := WinRMConfig{HTTPS: true}
	if got := cfg2.effectivePort(); got != defaultWinRMPortTLS {
		t.Fatalf("expected %d, got %d", defaultWinRMPortTLS, got)
	}
	cfg3 := WinRMConfig{Port: 9999}
	if got := cfg3.effectivePort(); got != 9999 {
		t.Fatalf("expected 9999, got %d", got)
	}
}

func TestWinRMConfigEffectiveTimeout(t *testing.T) {
	cfg1 := WinRMConfig{}
	if got := cfg1.effectiveTimeout(); got != 30*time.Second {
		t.Fatalf("expected 30s, got %v", got)
	}
	cfg2 := WinRMConfig{Timeout: 10 * time.Second}
	if got := cfg2.effectiveTimeout(); got != 10*time.Second {
		t.Fatalf("expected 10s, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// buildWinRMScript
// ---------------------------------------------------------------------------

func TestBuildWinRMScript_EmptyCommand(t *testing.T) {
	_, err := buildWinRMScript(&protocol.CommandPayload{Command: "  "})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBuildWinRMScript_PowershellPassesArgs(t *testing.T) {
	for _, cmd := range []string{"powershell", "powershell.exe", "pwsh", "pwsh.exe"} {
		script, err := buildWinRMScript(&protocol.CommandPayload{
			Command: cmd,
			Args:    []string{"Get-ComputerInfo"},
		})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", cmd, err)
		}
		if script != "Get-ComputerInfo" {
			t.Fatalf("%s: expected 'Get-ComputerInfo', got %q", cmd, script)
		}
	}
}

func TestBuildWinRMScript_PowershellRequiresArgs(t *testing.T) {
	_, err := buildWinRMScript(&protocol.CommandPayload{Command: "powershell"})
	if err == nil {
		t.Fatal("expected error for powershell with no args")
	}
}

func TestBuildWinRMScript_ArbitraryCommand(t *testing.T) {
	script, err := buildWinRMScript(&protocol.CommandPayload{
		Command: `C:\tools\myapp.exe`,
		Args:    []string{"--version"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "myapp.exe") {
		t.Fatalf("expected script to contain exe name, got %q", script)
	}
	if !strings.Contains(script, "--version") {
		t.Fatalf("expected script to contain args, got %q", script)
	}
}

func TestBuildWinRMScript_ArbitraryCommandNoArgs(t *testing.T) {
	script, err := buildWinRMScript(&protocol.CommandPayload{Command: "ipconfig"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "ipconfig") {
		t.Fatalf("expected script to contain 'ipconfig', got %q", script)
	}
}

func TestBuildWinRMScript_PowershellStripsCommandFlag(t *testing.T) {
	script, err := buildWinRMScript(&protocol.CommandPayload{
		Command: "powershell",
		Args:    []string{"-Command", "Get-Process"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if script != "Get-Process" {
		t.Fatalf("expected 'Get-Process', got %q", script)
	}
}

// ---------------------------------------------------------------------------
// WinRMExecutor.Execute
// ---------------------------------------------------------------------------

func TestWinRMExecutorExecute_Success(t *testing.T) {
	runner := newMockRunner(mockPSResponse{stdout: "hello\n", exitCode: 0})
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())

	res, err := exec.Execute(context.Background(), "Write-Output 'hello'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", res.ExitCode)
	}
	if res.Stdout != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", res.Stdout)
	}
	if res.Stderr != "" {
		t.Fatalf("expected empty stderr, got %q", res.Stderr)
	}
	if res.Truncated {
		t.Fatal("expected not truncated")
	}
}

func TestWinRMExecutorExecute_NonZeroExit(t *testing.T) {
	runner := newMockRunner(mockPSResponse{stderr: "access denied", exitCode: 5})
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())

	res, err := exec.Execute(context.Background(), "some-restricted-cmd")
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.ExitCode != 5 {
		t.Fatalf("expected exit 5, got %d", res.ExitCode)
	}
	if res.Stderr != "access denied" {
		t.Fatalf("expected 'access denied', got %q", res.Stderr)
	}
}

func TestWinRMExecutorExecute_TransportError(t *testing.T) {
	runner := newMockRunner(mockPSResponse{err: errors.New("connection refused")})
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())

	res, err := exec.Execute(context.Background(), "Get-Process")
	if err == nil {
		t.Fatal("expected transport error")
	}
	if res.ExitCode != -1 {
		t.Fatalf("expected exit -1 on error, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "connection refused") {
		t.Fatalf("expected error message in stderr, got %q", res.Stderr)
	}
}

func TestWinRMExecutorExecute_TruncatesLargeOutput(t *testing.T) {
	bigOutput := strings.Repeat("x", winRMMaxOutputSize+100)
	runner := newMockRunner(mockPSResponse{stdout: bigOutput, exitCode: 0})
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())

	res, err := exec.Execute(context.Background(), "echo big")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Stdout) != winRMMaxOutputSize {
		t.Fatalf("expected stdout truncated to %d, got %d", winRMMaxOutputSize, len(res.Stdout))
	}
	if !res.Truncated {
		t.Fatal("expected Truncated=true")
	}
}

func TestWinRMExecutorExecute_RequestIDPassedThrough(t *testing.T) {
	runner := newMockRunner(mockPSResponse{stdout: "ok", exitCode: 0})
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())

	// Execute itself doesn't set RequestID — that's handled by the adapter layer.
	// Confirm the result has a zero RequestID from the executor.
	res, _ := exec.Execute(context.Background(), "echo ok")
	if res.RequestID != "" {
		t.Fatalf("executor should not set RequestID, got %q", res.RequestID)
	}
}

func TestWinRMExecutorRunPS(t *testing.T) {
	runner := newMockRunner(mockPSResponse{stdout: "hostname\n", exitCode: 0})
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())

	stdout, stderr, code, err := exec.RunPS(context.Background(), "$env:COMPUTERNAME")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if stdout != "hostname\n" {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
}

func TestWinRMExecutorRunPS_PropagatesError(t *testing.T) {
	runner := newMockRunner(mockPSResponse{err: errors.New("timed out")})
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())

	_, _, code, err := exec.RunPS(context.Background(), "sleep 99")
	if err == nil {
		t.Fatal("expected error")
	}
	if code != -1 {
		t.Fatalf("expected -1 on error, got %d", code)
	}
}

// ---------------------------------------------------------------------------
// WinRMAdapter.Execute — policy enforcement
// ---------------------------------------------------------------------------

func makeAdapter(policy Policy) (*WinRMAdapter, *mockPSRunner) {
	runner := &mockPSRunner{}
	exec := newWinRMExecutorWithRunner(testWinRMCfg(), runner, nopLogger())
	adapter := NewWinRMAdapter(exec, policy, nopLogger())
	return adapter, runner
}

func TestWinRMAdapterExecute_Success(t *testing.T) {
	adapter, runner := makeAdapter(Policy{Level: protocol.CapRemediate})
	runner.queue = []mockPSResponse{{stdout: "result\n", exitCode: 0}}

	cmd := &protocol.CommandPayload{
		RequestID: "req-1",
		Command:   "powershell",
		Args:      []string{"Get-Process"},
		Level:     protocol.CapRemediate,
		Timeout:   5 * time.Second,
	}

	res := adapter.Execute(context.Background(), cmd)
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d: %s", res.ExitCode, res.Stderr)
	}
	if res.RequestID != "req-1" {
		t.Fatalf("expected request_id req-1, got %q", res.RequestID)
	}
	if res.Stdout != "result\n" {
		t.Fatalf("expected 'result\\n', got %q", res.Stdout)
	}
}

func TestWinRMAdapterExecute_PolicyBlocksByLevel(t *testing.T) {
	adapter, _ := makeAdapter(Policy{Level: protocol.CapObserve})

	// powershell is classified as CapRemediate (unknown command)
	cmd := &protocol.CommandPayload{
		RequestID: "req-2",
		Command:   "powershell",
		Args:      []string{"Remove-Item C:\\important"},
		Level:     protocol.CapObserve,
		Timeout:   5 * time.Second,
	}

	res := adapter.Execute(context.Background(), cmd)
	if res.ExitCode != -1 {
		t.Fatalf("expected blocked (exit -1), got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "policy violation") {
		t.Fatalf("expected policy violation message, got %q", res.Stderr)
	}
}

func TestWinRMAdapterExecute_BlockedCommand(t *testing.T) {
	adapter, _ := makeAdapter(Policy{
		Level:   protocol.CapRemediate,
		Blocked: []string{"powershell format-disk"},
	})

	cmd := &protocol.CommandPayload{
		RequestID: "req-3",
		Command:   "powershell",
		Args:      []string{"format-disk"},
		Level:     protocol.CapRemediate,
	}

	res := adapter.Execute(context.Background(), cmd)
	if res.ExitCode != -1 {
		t.Fatalf("expected blocked, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "blocked") {
		t.Fatalf("expected blocked message, got %q", res.Stderr)
	}
}

func TestWinRMAdapterExecute_AllowlistEnforced(t *testing.T) {
	adapter, _ := makeAdapter(Policy{
		Level:   protocol.CapRemediate,
		Allowed: []string{"powershell get-"},
	})

	cmd := &protocol.CommandPayload{
		RequestID: "req-4",
		Command:   "powershell",
		Args:      []string{"Set-ExecutionPolicy Unrestricted"},
		Level:     protocol.CapRemediate,
	}

	res := adapter.Execute(context.Background(), cmd)
	if res.ExitCode != -1 {
		t.Fatalf("expected blocked by allowlist, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "allowlist") {
		t.Fatalf("expected allowlist message, got %q", res.Stderr)
	}
}

func TestWinRMAdapterExecute_AllowlistPermits(t *testing.T) {
	adapter, runner := makeAdapter(Policy{
		Level:   protocol.CapRemediate,
		Allowed: []string{"powershell get-"},
	})
	runner.queue = []mockPSResponse{{stdout: "services\n", exitCode: 0}}

	cmd := &protocol.CommandPayload{
		RequestID: "req-5",
		Command:   "powershell",
		Args:      []string{"get-service"},
		Level:     protocol.CapRemediate,
	}

	res := adapter.Execute(context.Background(), cmd)
	if res.ExitCode != 0 {
		t.Fatalf("expected allowed, got %d: %s", res.ExitCode, res.Stderr)
	}
}

func TestWinRMAdapterExecute_EmptyCommand(t *testing.T) {
	adapter, _ := makeAdapter(Policy{Level: protocol.CapRemediate})

	cmd := &protocol.CommandPayload{
		RequestID: "req-6",
		Command:   "   ",
		Level:     protocol.CapRemediate,
	}

	res := adapter.Execute(context.Background(), cmd)
	if res.ExitCode != -1 {
		t.Fatalf("expected error exit for empty command, got %d", res.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// WinRMPool
// ---------------------------------------------------------------------------

func TestWinRMPoolGet_ReturnsCachedInstance(t *testing.T) {
	pool := NewWinRMPool(nopLogger())
	cfg := testWinRMCfg()

	ex1, err := pool.Get(cfg)
	if err != nil {
		t.Fatalf("pool.Get: %v", err)
	}
	ex2, err := pool.Get(cfg)
	if err != nil {
		t.Fatalf("pool.Get second: %v", err)
	}
	if ex1 != ex2 {
		t.Fatal("expected same executor instance from pool")
	}
}

func TestWinRMPoolEvict_RemovesFromCache(t *testing.T) {
	pool := NewWinRMPool(nopLogger())
	cfg := testWinRMCfg()

	ex1, _ := pool.Get(cfg)
	pool.Evict(cfg)
	ex2, _ := pool.Get(cfg)
	if ex1 == ex2 {
		t.Fatal("expected fresh executor after evict")
	}
}

func TestWinRMPoolGet_ValidationError(t *testing.T) {
	pool := NewWinRMPool(nopLogger())
	_, err := pool.Get(WinRMConfig{Host: "h"}) // missing user/password
	if err == nil {
		t.Fatal("expected validation error for incomplete config")
	}
}
