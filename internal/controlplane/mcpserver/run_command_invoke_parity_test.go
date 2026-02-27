package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

type runCommandInvokeParityDispatcher struct {
	envelope *corecommanddispatch.CommandResultEnvelope
	probeID  string
	cmd      protocol.CommandPayload
	policy   corecommanddispatch.DispatchPolicy
}

func (d *runCommandInvokeParityDispatcher) DispatchWithPolicy(_ context.Context, probeID string, cmd protocol.CommandPayload, policy corecommanddispatch.DispatchPolicy) *corecommanddispatch.CommandResultEnvelope {
	d.probeID = probeID
	d.cmd = cmd
	d.policy = policy
	return d.envelope
}

func TestRunCommandInvokeMCP_ParityWithLegacyPath(t *testing.T) {
	tests := []struct {
		name     string
		envelope *corecommanddispatch.CommandResultEnvelope
	}{
		{
			name: "success",
			envelope: &corecommanddispatch.CommandResultEnvelope{Result: &protocol.CommandResultPayload{
				ExitCode: 0,
				Stdout:   " ok ",
			}},
		},
		{
			name:     "dispatch_error",
			envelope: &corecommanddispatch.CommandResultEnvelope{Err: errors.New("not connected")},
		},
		{
			name:     "timeout",
			envelope: &corecommanddispatch.CommandResultEnvelope{Err: corecommanddispatch.ErrTimeout},
		},
		{
			name:     "nil_envelope",
			envelope: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyDispatcher := &runCommandInvokeParityDispatcher{envelope: tc.envelope}
			legacyEnvelope := legacyInvokeRunCommandMCP(context.Background(), legacyDispatcher, "probe-mcp", "hostname", protocol.CapObserve)

			adapterDispatcher := &runCommandInvokeParityDispatcher{envelope: tc.envelope}
			invokeInput := corecommanddispatch.AssembleCommandInvokeMCP("probe-mcp", "hostname", protocol.CapObserve)
			projection := corecommanddispatch.InvokeCommandForSurface(context.Background(), invokeInput, adapterDispatcher)

			if legacyDispatcher.probeID != adapterDispatcher.probeID {
				t.Fatalf("probe mismatch: legacy=%q adapter=%q", legacyDispatcher.probeID, adapterDispatcher.probeID)
			}
			if legacyDispatcher.cmd.Command != adapterDispatcher.cmd.Command || legacyDispatcher.cmd.Level != adapterDispatcher.cmd.Level {
				t.Fatalf("command mismatch: legacy=%+v adapter=%+v", legacyDispatcher.cmd, adapterDispatcher.cmd)
			}
			if legacyDispatcher.cmd.RequestID == "" || adapterDispatcher.cmd.RequestID == "" {
				t.Fatalf("expected generated request ids, legacy=%q adapter=%q", legacyDispatcher.cmd.RequestID, adapterDispatcher.cmd.RequestID)
			}
			if !strings.HasPrefix(legacyDispatcher.cmd.RequestID, "cmd-") || !strings.HasPrefix(adapterDispatcher.cmd.RequestID, "cmd-") {
				t.Fatalf("expected cmd- prefix request ids, legacy=%q adapter=%q", legacyDispatcher.cmd.RequestID, adapterDispatcher.cmd.RequestID)
			}
			if legacyDispatcher.policy != adapterDispatcher.policy {
				t.Fatalf("policy mismatch: legacy=%+v adapter=%+v", legacyDispatcher.policy, adapterDispatcher.policy)
			}

			legacyResult, legacyMeta, legacyErr := legacyRenderRunCommandMCP(legacyEnvelope)
			adapterResult, adapterMeta, adapterErr := renderRunCommandMCP(projection)

			if (legacyErr == nil) != (adapterErr == nil) {
				t.Fatalf("error presence mismatch: legacy=%v adapter=%v", legacyErr, adapterErr)
			}
			if legacyErr != nil && adapterErr != nil {
				if !errors.Is(adapterErr, legacyErr) && adapterErr.Error() != legacyErr.Error() {
					t.Fatalf("error mismatch: legacy=%v adapter=%v", legacyErr, adapterErr)
				}
			}
			if (legacyResult == nil) != (adapterResult == nil) {
				t.Fatalf("result presence mismatch: legacy=%#v adapter=%#v", legacyResult, adapterResult)
			}
			if legacyResult != nil {
				if gotLegacy, gotAdapter := toolText(t, legacyResult), toolText(t, adapterResult); gotLegacy != gotAdapter {
					t.Fatalf("tool text mismatch: legacy=%q adapter=%q", gotLegacy, gotAdapter)
				}
			}
			if legacyMeta != nil || adapterMeta != nil {
				t.Fatalf("expected nil meta parity, legacy=%#v adapter=%#v", legacyMeta, adapterMeta)
			}
		})
	}
}

func legacyInvokeRunCommandMCP(ctx context.Context, dispatcher interface {
	DispatchWithPolicy(ctx context.Context, probeID string, cmd protocol.CommandPayload, policy corecommanddispatch.DispatchPolicy) *corecommanddispatch.CommandResultEnvelope
}, probeID, command string, level protocol.CapabilityLevel) *corecommanddispatch.CommandResultEnvelope {
	cmd := protocol.CommandPayload{
		RequestID: corecommanddispatch.NextCommandRequestID(),
		Command:   command,
		Level:     level,
	}
	return dispatcher.DispatchWithPolicy(ctx, probeID, cmd, corecommanddispatch.WaitPolicy(30*time.Second))
}
