package commanddispatch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestDispatchWithPolicy_DispatchOnlyAppliesStream(t *testing.T) {
	tracker := cmdtracker.New(time.Minute)
	var sent protocol.CommandPayload
	svc := NewService(&stubSender{sendFn: func(_ string, _ protocol.MessageType, payload any) error {
		cmd, ok := payload.(protocol.CommandPayload)
		if !ok {
			t.Fatalf("expected protocol.CommandPayload, got %T", payload)
		}
		sent = cmd
		return nil
	}}, tracker)

	env := svc.DispatchWithPolicy(context.Background(), "probe-1", protocol.CommandPayload{
		RequestID: "req-stream",
		Command:   "tail -f /var/log/syslog",
		Level:     protocol.CapObserve,
	}, DispatchOnlyPolicy(true))

	if env == nil || env.Err != nil {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if env.State != ResultStateDispatched || !env.Dispatched {
		t.Fatalf("unexpected dispatch state: %+v", env)
	}
	if !sent.Stream {
		t.Fatal("expected stream=true payload when StreamOutput policy is enabled")
	}
}

func TestDispatchWithPolicy_TimeoutMapsToHTTPContract(t *testing.T) {
	tracker := cmdtracker.New(time.Minute)
	svc := NewService(&stubSender{}, tracker)

	env := svc.DispatchWithPolicy(context.Background(), "probe-1", protocol.CommandPayload{
		RequestID: "req-timeout-policy",
		Command:   "ls",
		Level:     protocol.CapObserve,
	}, WaitPolicy(15*time.Millisecond))

	if env == nil {
		t.Fatal("expected envelope")
	}
	if !errors.Is(env.Err, ErrTimeout) || env.State != ResultStateTimeout {
		t.Fatalf("expected timeout envelope, got %+v", env)
	}

	httpErr, ok := env.HTTPError()
	if !ok {
		t.Fatal("expected HTTP error mapping")
	}
	if httpErr.Status != http.StatusGatewayTimeout || httpErr.Code != "timeout" {
		t.Fatalf("unexpected HTTP mapping: %+v", httpErr)
	}
}

func TestDispatchWithPolicy_ContextCancelRespectsPolicy(t *testing.T) {
	tracker := cmdtracker.New(time.Minute)
	svc := NewService(&stubSender{}, tracker)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := svc.DispatchWithPolicy(ctx, "probe-1", protocol.CommandPayload{
		RequestID: "req-cancel-policy",
		Command:   "ls",
		Level:     protocol.CapObserve,
	}, DispatchPolicy{WaitForResult: true, Timeout: time.Second, CancelOnContextDone: false})

	if env == nil {
		t.Fatal("expected envelope")
	}
	if !errors.Is(env.Err, context.Canceled) || env.State != ResultStateCanceled {
		t.Fatalf("expected canceled envelope, got %+v", env)
	}
	if tracker.InFlight() != 1 {
		t.Fatalf("expected pending command to remain tracked when cancel_on_context_done=false, got %d", tracker.InFlight())
	}

	tracker.Cancel("req-cancel-policy")
}

func TestCommandResultEnvelopeMCPErrorMapping(t *testing.T) {
	timeoutEnv := &CommandResultEnvelope{Err: ErrTimeout}
	if err := timeoutEnv.MCPError(); !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected timeout passthrough, got %v", err)
	}

	dispatchEnv := &CommandResultEnvelope{Err: errors.New("not connected")}
	err := dispatchEnv.MCPError()
	if err == nil || !strings.Contains(err.Error(), "dispatch command: not connected") {
		t.Fatalf("expected wrapped dispatch error, got %v", err)
	}
}
