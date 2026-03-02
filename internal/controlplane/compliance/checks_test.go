package compliance

import (
	"context"
	"fmt"
	"testing"
)

// mockExecutor creates a ProbeExecutor that returns canned output.
func mockExec(output string, exitCode int) ProbeExecutor {
	return func(_ context.Context, _ string) (string, int, error) {
		return output, exitCode, nil
	}
}

// failExec creates a ProbeExecutor that always returns an execution error.
func failExec() ProbeExecutor {
	return func(_ context.Context, _ string) (string, int, error) {
		return "", -1, fmt.Errorf("command not available")
	}
}

func TestCheckSSHPasswordAuth(t *testing.T) {
	tests := []struct {
		name       string
		exec       ProbeExecutor
		wantStatus string
	}{
		{
			name:       "disabled",
			exec:       mockExec("no\n", 0),
			wantStatus: StatusPass,
		},
		{
			name:       "enabled",
			exec:       mockExec("yes\n", 0),
			wantStatus: StatusFail,
		},
		{
			name:       "empty output",
			exec:       mockExec("", 0),
			wantStatus: StatusWarning,
		},
		{
			name:       "command fails",
			exec:       failExec(),
			wantStatus: StatusUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, evidence, err := checkSSHPasswordAuth(context.Background(), tt.exec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tt.wantStatus {
				t.Errorf("expected status %q, got %q (evidence: %s)", tt.wantStatus, status, evidence)
			}
		})
	}
}

func TestCheckSSHRootLogin(t *testing.T) {
	tests := []struct {
		name       string
		exec       ProbeExecutor
		wantStatus string
	}{
		{
			name:       "disabled",
			exec:       mockExec("no\n", 0),
			wantStatus: StatusPass,
		},
		{
			name:       "enabled",
			exec:       mockExec("yes\n", 0),
			wantStatus: StatusFail,
		},
		{
			name:       "prohibit-password",
			exec:       mockExec("prohibit-password\n", 0),
			wantStatus: StatusWarning,
		},
		{
			name:       "without-password",
			exec:       mockExec("without-password\n", 0),
			wantStatus: StatusWarning,
		},
		{
			name:       "unknown value",
			exec:       mockExec("forced-commands-only\n", 0),
			wantStatus: StatusUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, evidence, err := checkSSHRootLogin(context.Background(), tt.exec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tt.wantStatus {
				t.Errorf("expected status %q, got %q (evidence: %s)", tt.wantStatus, status, evidence)
			}
		})
	}
}

func TestCheckPasswordlessAccounts(t *testing.T) {
	tests := []struct {
		name       string
		exec       ProbeExecutor
		wantStatus string
	}{
		{
			name:       "no passwordless accounts",
			exec:       mockExec("", 0),
			wantStatus: StatusPass,
		},
		{
			name:       "passwordless account found",
			exec:       mockExec("baduser\n", 0),
			wantStatus: StatusFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _, err := checkPasswordlessAccounts(context.Background(), tt.exec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tt.wantStatus {
				t.Errorf("expected %q, got %q", tt.wantStatus, status)
			}
		})
	}
}

func TestBuiltinChecksCount(t *testing.T) {
	checks := BuiltinChecks()
	if len(checks) < 6 {
		t.Errorf("expected at least 6 builtin checks, got %d", len(checks))
	}

	// Check for required IDs
	required := map[string]bool{
		"os-patching":          false,
		"ssh-password-auth":    false,
		"ssh-root-login":       false,
		"firewall-active":      false,
		"disk-encryption":      false,
		"passwordless-accounts": false,
		"unnecessary-services": false,
	}

	for _, c := range checks {
		if _, ok := required[c.ID]; ok {
			required[c.ID] = true
		}
	}

	for id, found := range required {
		if !found {
			t.Errorf("missing builtin check: %s", id)
		}
	}
}
