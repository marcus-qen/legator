package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func TestExecute_ObserveLevel(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, testLogger())

	cmd := &protocol.CommandPayload{
		RequestID: "test-1",
		Command:   "echo",
		Args:      []string{"hello"},
		Level:     protocol.CapObserve,
		Timeout:   5 * time.Second,
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d: %s", result.ExitCode, result.Stderr)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", result.Stdout)
	}
}

func TestExecute_BlockedByLevel(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, testLogger())

	cmd := &protocol.CommandPayload{
		RequestID: "test-2",
		Command:   "rm",
		Args:      []string{"-rf", "/tmp/test"},
		Level:     protocol.CapRemediate,
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != -1 {
		t.Errorf("expected blocked (exit -1), got %d", result.ExitCode)
	}
	if result.Stderr == "" {
		t.Error("expected policy violation message")
	}
}

func TestExecute_BlockedCommand(t *testing.T) {
	e := New(Policy{
		Level:   protocol.CapRemediate,
		Blocked: []string{"rm -rf /"},
	}, testLogger())

	cmd := &protocol.CommandPayload{
		RequestID: "test-3",
		Command:   "rm",
		Args:      []string{"-rf", "/"},
		Level:     protocol.CapRemediate,
	}

	// The blocked check uses the full command string
	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != -1 {
		t.Errorf("expected blocked, got exit %d", result.ExitCode)
	}
}

func TestExecute_Allowlist(t *testing.T) {
	e := New(Policy{
		Level:   protocol.CapDiagnose,
		Allowed: []string{"echo", "cat"},
	}, testLogger())

	// Allowed
	cmd := &protocol.CommandPayload{
		RequestID: "test-4a",
		Command:   "echo",
		Args:      []string{"ok"},
		Level:     protocol.CapDiagnose,
		Timeout:   5 * time.Second,
	}
	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != 0 {
		t.Errorf("echo should be allowed, got exit %d: %s", result.ExitCode, result.Stderr)
	}

	// Not allowed
	cmd2 := &protocol.CommandPayload{
		RequestID: "test-4b",
		Command:   "ls",
		Level:     protocol.CapDiagnose,
	}
	result2 := e.Execute(context.Background(), cmd2)
	if result2.ExitCode != -1 {
		t.Errorf("ls should be blocked by allowlist, got exit %d", result2.ExitCode)
	}
}

func TestExecute_Timeout(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, testLogger())

	cmd := &protocol.CommandPayload{
		RequestID: "test-5",
		Command:   "sleep",
		Args:      []string{"10"},
		Level:     protocol.CapObserve,
		Timeout:   100 * time.Millisecond,
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit for timed-out command")
	}
	if result.Duration > 5000 {
		t.Errorf("command took too long: %dms", result.Duration)
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 3) != "hel" {
		t.Error("truncate failed")
	}
	if truncate("hi", 10) != "hi" {
		t.Error("no-op truncate failed")
	}
}

func TestExecute_ClassifierOverridesDeclaredLevel(t *testing.T) {
	// Probe is at observe level
	e := New(Policy{Level: protocol.CapObserve}, testLogger())

	// Command declares observe but is actually remediate (touch)
	cmd := &protocol.CommandPayload{
		RequestID: "test-defence",
		Command:   "touch",
		Args:      []string{"/tmp/evil"},
		Level:     protocol.CapObserve, // lies about level
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != -1 {
		t.Error("expected command to be blocked")
	}
	if !strings.Contains(result.Stderr, "policy violation") {
		t.Errorf("expected policy violation in stderr, got: %s", result.Stderr)
	}
	if !strings.Contains(result.Stderr, "remediate") {
		t.Errorf("expected classified level in error, got: %s", result.Stderr)
	}
}

func TestExecute_ClassifierAllowsLegitObserve(t *testing.T) {
	// Probe at observe level, command is legitimately observe
	e := New(Policy{Level: protocol.CapObserve}, testLogger())

	cmd := &protocol.CommandPayload{
		RequestID: "test-legit",
		Command:   "hostname",
		Level:     protocol.CapObserve,
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != 0 {
		t.Errorf("expected exit 0, got %d: %s", result.ExitCode, result.Stderr)
	}
}

func TestExecute_BlocksHostDirectMutationBypass(t *testing.T) {
	e := New(Policy{Level: protocol.CapRemediate}, testLogger())

	cmd := &protocol.CommandPayload{
		RequestID:      "test-host-direct-bypass",
		Command:        "touch",
		Args:           []string{t.TempDir() + "/blocked"},
		Level:          protocol.CapObserve,
		ExecutionClass: protocol.ExecObserveDirect,
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != -1 {
		t.Fatalf("expected blocked command, got exit %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "host_direct lane") {
		t.Fatalf("expected host_direct lane policy violation, got %q", result.Stderr)
	}
}

func TestExecute_BlocksBreakglassWithoutTypedConfirmation(t *testing.T) {
	e := New(Policy{
		Level: protocol.CapRemediate,
		Breakglass: protocol.BreakglassPolicy{
			Enabled:                  true,
			AllowedReasons:           []string{"incident_response"},
			RequireTypedConfirmation: true,
		},
	}, testLogger())

	cmd := &protocol.CommandPayload{
		RequestID:      "test-breakglass-missing-confirmation",
		Command:        "touch",
		Args:           []string{t.TempDir() + "/blocked"},
		Level:          protocol.CapObserve,
		ExecutionClass: protocol.ExecBreakglassDirect,
		Breakglass: &protocol.BreakglassInvocation{
			Reason: "incident_response",
		},
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != -1 {
		t.Fatalf("expected blocked command, got exit %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "typed confirmation") {
		t.Fatalf("expected typed confirmation policy violation, got %q", result.Stderr)
	}
}

func TestExecute_AllowsBreakglassWithTypedConfirmation(t *testing.T) {
	e := New(Policy{
		Level: protocol.CapRemediate,
		Breakglass: protocol.BreakglassPolicy{
			Enabled:                  true,
			AllowedReasons:           []string{"incident_response"},
			RequireTypedConfirmation: true,
		},
	}, testLogger())

	tmpFile := t.TempDir() + "/created-by-breakglass"
	cmd := &protocol.CommandPayload{
		RequestID:      "test-breakglass-valid",
		Command:        "touch",
		Args:           []string{tmpFile},
		Level:          protocol.CapObserve,
		ExecutionClass: protocol.ExecBreakglassDirect,
		Breakglass: &protocol.BreakglassInvocation{
			Reason:            "incident_response",
			TypedConfirmation: protocol.BreakglassTypedConfirmationPhrase,
		},
		Timeout: 5 * time.Second,
	}

	result := e.Execute(context.Background(), cmd)
	if result.ExitCode != 0 {
		t.Fatalf("expected breakglass command to succeed, got exit %d stderr=%q", result.ExitCode, result.Stderr)
	}
	if _, err := os.Stat(tmpFile); err != nil {
		t.Fatalf("expected touched file to exist: %v", err)
	}
}
