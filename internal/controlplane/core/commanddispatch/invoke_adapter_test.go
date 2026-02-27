package commanddispatch

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

type invokeAdapterStub struct {
	probeID string
	cmd     protocol.CommandPayload
	policy  DispatchPolicy
	env     *CommandResultEnvelope
}

func (s *invokeAdapterStub) DispatchWithPolicy(_ context.Context, probeID string, cmd protocol.CommandPayload, policy DispatchPolicy) *CommandResultEnvelope {
	s.probeID = probeID
	s.cmd = cmd
	s.policy = policy
	return s.env
}

func TestAssembleCommandInvokeHTTP_ParityWithLegacyPolicySelection(t *testing.T) {
	tests := []struct {
		name       string
		cmd        protocol.CommandPayload
		wantWait   bool
		wantStream bool
		wantPolicy DispatchPolicy
	}{
		{
			name:       "dispatch_only_stream",
			cmd:        protocol.CommandPayload{RequestID: "req-http-1", Command: "ls", Timeout: 2 * time.Second},
			wantWait:   false,
			wantStream: true,
			wantPolicy: DispatchOnlyPolicy(true),
		},
		{
			name:       "wait_stream_default_timeout",
			cmd:        protocol.CommandPayload{RequestID: "req-http-2", Command: "ls"},
			wantWait:   true,
			wantStream: true,
			wantPolicy: DispatchPolicy{WaitForResult: true, StreamOutput: true, Timeout: 30 * time.Second, CancelOnContextDone: true},
		},
		{
			name:       "wait_custom_timeout",
			cmd:        protocol.CommandPayload{RequestID: "req-http-3", Command: "ls", Timeout: 3 * time.Second},
			wantWait:   true,
			wantStream: false,
			wantPolicy: DispatchPolicy{WaitForResult: true, StreamOutput: false, Timeout: 8 * time.Second, CancelOnContextDone: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			invokeInput := AssembleCommandInvokeHTTP("probe-http", tc.cmd, tc.wantWait, tc.wantStream)
			if invokeInput == nil {
				t.Fatal("expected invoke input")
			}
			if invokeInput.Surface != ProjectionDispatchSurfaceHTTP {
				t.Fatalf("expected HTTP surface, got %q", invokeInput.Surface)
			}
			if invokeInput.Command.RequestID != tc.cmd.RequestID {
				t.Fatalf("expected request-id parity, got %q want %q", invokeInput.Command.RequestID, tc.cmd.RequestID)
			}
			if invokeInput.Policy != tc.wantPolicy {
				t.Fatalf("unexpected policy: got %+v want %+v", invokeInput.Policy, tc.wantPolicy)
			}
		})
	}
}

func TestAssembleCommandInvokeHTTP_GeneratesRequestIDWhenMissing(t *testing.T) {
	invokeInput := AssembleCommandInvokeHTTP("probe-http", protocol.CommandPayload{Command: "ls"}, false, false)
	if invokeInput == nil {
		t.Fatal("expected invoke input")
	}
	if invokeInput.Command.RequestID == "" {
		t.Fatal("expected generated request id")
	}
	if !strings.HasPrefix(invokeInput.Command.RequestID, "cmd-") {
		t.Fatalf("expected cmd- prefix, got %q", invokeInput.Command.RequestID)
	}
	if _, err := strconv.Atoi(strings.TrimPrefix(invokeInput.Command.RequestID, "cmd-")); err != nil {
		t.Fatalf("expected numeric request-id suffix, got %q (%v)", invokeInput.Command.RequestID, err)
	}
}

func TestAssembleCommandInvokeMCP_ParityWithLegacyPolicySelection(t *testing.T) {
	invokeInput := AssembleCommandInvokeMCP("probe-mcp", "uname -a", protocol.CapObserve)
	if invokeInput == nil {
		t.Fatal("expected invoke input")
	}
	if invokeInput.Surface != ProjectionDispatchSurfaceMCP {
		t.Fatalf("expected MCP surface, got %q", invokeInput.Surface)
	}
	if invokeInput.ProbeID != "probe-mcp" {
		t.Fatalf("expected probe id probe-mcp, got %q", invokeInput.ProbeID)
	}
	if invokeInput.Command.Command != "uname -a" {
		t.Fatalf("expected command parity, got %q", invokeInput.Command.Command)
	}
	if invokeInput.Command.Level != protocol.CapObserve {
		t.Fatalf("expected command level observe, got %q", invokeInput.Command.Level)
	}
	if invokeInput.Command.RequestID == "" {
		t.Fatal("expected generated request id")
	}
	wantPolicy := WaitPolicy(30 * time.Second)
	if invokeInput.Policy != wantPolicy {
		t.Fatalf("unexpected MCP policy: got %+v want %+v", invokeInput.Policy, wantPolicy)
	}
}

func TestInvokeCommandForSurface_ParityWithLegacyDispatchPath(t *testing.T) {
	stub := &invokeAdapterStub{env: &CommandResultEnvelope{RequestID: "req-adapter", Dispatched: true}}
	invokeInput := &CommandInvokeInput{
		ProbeID: "probe-invoke",
		Command: protocol.CommandPayload{RequestID: "req-adapter", Command: "hostname", Level: protocol.CapObserve},
		Policy:  DispatchPolicy{WaitForResult: false, StreamOutput: true},
		Surface: ProjectionDispatchSurfaceHTTP,
	}

	projection := InvokeCommandForSurface(context.Background(), invokeInput, stub)
	if projection == nil {
		t.Fatal("expected invoke projection")
	}
	if projection.RequestID != "req-adapter" {
		t.Fatalf("expected projection request id req-adapter, got %q", projection.RequestID)
	}
	if projection.Surface != ProjectionDispatchSurfaceHTTP {
		t.Fatalf("expected projection surface HTTP, got %q", projection.Surface)
	}
	if projection.WaitForResult {
		t.Fatal("expected wait=false projection")
	}
	if projection.Envelope != stub.env {
		t.Fatalf("expected envelope passthrough, got %+v want %+v", projection.Envelope, stub.env)
	}

	if stub.probeID != "probe-invoke" {
		t.Fatalf("expected probe id parity, got %q", stub.probeID)
	}
	if stub.cmd.RequestID != "req-adapter" || stub.cmd.Command != "hostname" {
		t.Fatalf("unexpected dispatched command payload: %+v", stub.cmd)
	}
	if stub.policy != invokeInput.Policy {
		t.Fatalf("unexpected dispatched policy: got %+v want %+v", stub.policy, invokeInput.Policy)
	}
}
