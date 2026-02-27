package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

type commandInvokeParityDispatcher struct {
	envelope *corecommanddispatch.CommandResultEnvelope
	probeID  string
	cmd      protocol.CommandPayload
	policy   corecommanddispatch.DispatchPolicy
}

func (d *commandInvokeParityDispatcher) DispatchWithPolicy(_ context.Context, probeID string, cmd protocol.CommandPayload, policy corecommanddispatch.DispatchPolicy) *corecommanddispatch.CommandResultEnvelope {
	d.probeID = probeID
	d.cmd = cmd
	d.policy = policy
	return d.envelope
}

func TestCommandInvokeHTTP_ParityWithLegacyPath(t *testing.T) {
	tests := []struct {
		name       string
		cmd        protocol.CommandPayload
		wantWait   bool
		wantStream bool
		envelope   *corecommanddispatch.CommandResultEnvelope
	}{
		{
			name:       "dispatch_only_success",
			cmd:        protocol.CommandPayload{RequestID: "req-http-dispatch", Command: "hostname", Level: protocol.CapObserve},
			wantWait:   false,
			wantStream: true,
			envelope:   &corecommanddispatch.CommandResultEnvelope{Dispatched: true},
		},
		{
			name:       "wait_success",
			cmd:        protocol.CommandPayload{RequestID: "req-http-wait", Command: "uname -a", Level: protocol.CapObserve, Timeout: 2 * time.Second},
			wantWait:   true,
			wantStream: true,
			envelope: &corecommanddispatch.CommandResultEnvelope{Result: &protocol.CommandResultPayload{
				RequestID: "req-http-wait",
				ExitCode:  0,
				Stdout:    "ok",
			}},
		},
		{
			name:       "dispatch_error",
			cmd:        protocol.CommandPayload{RequestID: "req-http-error", Command: "id", Level: protocol.CapObserve},
			wantWait:   false,
			wantStream: false,
			envelope:   &corecommanddispatch.CommandResultEnvelope{Err: errors.New("not connected")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyDispatcher := &commandInvokeParityDispatcher{envelope: tc.envelope}
			legacyRequestID, legacyEnvelope := legacyInvokeDispatchCommandHTTP(context.Background(), legacyDispatcher, "probe-http", tc.cmd, tc.wantWait, tc.wantStream)

			adapterDispatcher := &commandInvokeParityDispatcher{envelope: tc.envelope}
			invokeInput := corecommanddispatch.AssembleCommandInvokeHTTP("probe-http", tc.cmd, tc.wantWait, tc.wantStream)
			projection := corecommanddispatch.InvokeCommandForSurface(context.Background(), invokeInput, adapterDispatcher)

			if legacyDispatcher.probeID != adapterDispatcher.probeID {
				t.Fatalf("probe mismatch: legacy=%q adapter=%q", legacyDispatcher.probeID, adapterDispatcher.probeID)
			}
			if !reflect.DeepEqual(legacyDispatcher.cmd, adapterDispatcher.cmd) {
				t.Fatalf("command mismatch: legacy=%+v adapter=%+v", legacyDispatcher.cmd, adapterDispatcher.cmd)
			}
			if legacyDispatcher.policy != adapterDispatcher.policy {
				t.Fatalf("policy mismatch: legacy=%+v adapter=%+v", legacyDispatcher.policy, adapterDispatcher.policy)
			}

			legacyRR := httptest.NewRecorder()
			legacyRenderDispatchCommandHTTP(legacyRR, legacyRequestID, legacyEnvelope, tc.wantWait)

			adapterRR := httptest.NewRecorder()
			renderDispatchCommandHTTP(adapterRR, projection)

			if legacyRR.Code != adapterRR.Code {
				t.Fatalf("status mismatch: legacy=%d adapter=%d", legacyRR.Code, adapterRR.Code)
			}
			if legacyRR.Body.String() != adapterRR.Body.String() {
				t.Fatalf("body mismatch:\nlegacy=%s\nadapter=%s", legacyRR.Body.String(), adapterRR.Body.String())
			}
			if !reflect.DeepEqual(legacyRR.Header(), adapterRR.Header()) {
				t.Fatalf("header mismatch: legacy=%v adapter=%v", legacyRR.Header(), adapterRR.Header())
			}
		})
	}
}

func legacyInvokeDispatchCommandHTTP(ctx context.Context, dispatcher interface {
	DispatchWithPolicy(ctx context.Context, probeID string, cmd protocol.CommandPayload, policy corecommanddispatch.DispatchPolicy) *corecommanddispatch.CommandResultEnvelope
}, probeID string, cmd protocol.CommandPayload, wantWait, wantStream bool) (string, *corecommanddispatch.CommandResultEnvelope) {
	if cmd.RequestID == "" {
		cmd.RequestID = corecommanddispatch.NextCommandRequestID()
	}

	timeout := 30 * time.Second
	if cmd.Timeout > 0 {
		timeout = cmd.Timeout + 5*time.Second
	}

	policy := corecommanddispatch.DispatchOnlyPolicy(wantStream)
	if wantWait {
		policy = corecommanddispatch.WaitPolicy(timeout)
		policy.StreamOutput = wantStream
	}

	envelope := dispatcher.DispatchWithPolicy(ctx, probeID, cmd, policy)
	return cmd.RequestID, envelope
}
