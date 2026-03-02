package agent

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func TestHandleMessagePolicyUpdatePersistsV2PolicyFields(t *testing.T) {
	configDir := t.TempDir()
	cfg := &Config{
		ServerURL: "https://example.test",
		ProbeID:   "probe-policy",
		APIKey:    "api-key",
		ConfigDir: configDir,
	}

	agent := New(cfg, zap.NewNop())
	agent.handleMessage(protocol.Envelope{
		Type: protocol.MsgPolicyUpdate,
		Payload: protocol.PolicyUpdatePayload{
			PolicyID:               "policy-v2",
			Level:                  protocol.CapDiagnose,
			Allowed:                []string{"ls"},
			Blocked:                []string{"rm"},
			Paths:                  []string{"/etc"},
			ExecutionClassRequired: protocol.ExecDiagnoseSandbox,
			SandboxRequired:        true,
			ApprovalMode:           protocol.ApprovalMutationGate,
			Breakglass:             protocol.BreakglassPolicy{Enabled: true, AllowedReasons: []string{"incident_response"}, RequireTypedConfirmation: true},
			MaxRuntimeSec:          120,
			AllowedScopes:          []string{"fleet.read", "command.exec"},
		},
	})

	loaded, err := LoadConfig(configDir)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}

	if loaded.PolicyID != "policy-v2" || loaded.PolicyLevel != protocol.CapDiagnose {
		t.Fatalf("expected persisted policy id/level, got %+v", loaded)
	}
	if loaded.PolicyExecutionClassRequired != protocol.ExecDiagnoseSandbox || !loaded.PolicySandboxRequired || loaded.PolicyApprovalMode != protocol.ApprovalMutationGate {
		t.Fatalf("expected persisted policy v2 scalar fields, got %+v", loaded)
	}
	if !loaded.PolicyBreakglass.Enabled || len(loaded.PolicyBreakglass.AllowedReasons) != 1 || loaded.PolicyBreakglass.AllowedReasons[0] != "incident_response" {
		t.Fatalf("expected persisted breakglass, got %+v", loaded.PolicyBreakglass)
	}
	if loaded.PolicyMaxRuntimeSec != 120 {
		t.Fatalf("expected policy_max_runtime_sec=120, got %d", loaded.PolicyMaxRuntimeSec)
	}
	if len(loaded.PolicyAllowedScopes) != 2 || loaded.PolicyAllowedScopes[1] != "command.exec" {
		t.Fatalf("expected persisted allowed scopes, got %v", loaded.PolicyAllowedScopes)
	}
}
