package commanddispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/protocol"
)

type stubSender struct {
	sendFn func(probeID string, msgType protocol.MessageType, payload any) error
}

func (s *stubSender) SendTo(probeID string, msgType protocol.MessageType, payload any) error {
	if s.sendFn != nil {
		return s.sendFn(probeID, msgType, payload)
	}
	return nil
}

func TestDispatchAndWait_Success(t *testing.T) {
	tracker := cmdtracker.New(time.Minute)
	sender := &stubSender{sendFn: func(_ string, _ protocol.MessageType, payload any) error {
		cmd, ok := payload.(protocol.CommandPayload)
		if !ok {
			t.Fatalf("expected protocol.CommandPayload, got %T", payload)
		}
		go func() {
			_ = tracker.Complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 0, Stdout: "ok"})
		}()
		return nil
	}}
	svc := NewService(sender, tracker)

	cmd := protocol.CommandPayload{RequestID: "req-success", Command: "ls", Level: protocol.CapObserve}
	result, err := svc.DispatchAndWait(context.Background(), "probe-1", cmd, 2*time.Second)
	if err != nil {
		t.Fatalf("DispatchAndWait returned error: %v", err)
	}
	if result == nil || result.Stdout != "ok" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDispatchTracked_SendErrorCancelsPending(t *testing.T) {
	tracker := cmdtracker.New(time.Minute)
	svc := NewService(&stubSender{sendFn: func(_ string, _ protocol.MessageType, _ any) error {
		return fmt.Errorf("not connected")
	}}, tracker)

	_, err := svc.DispatchTracked("probe-1", protocol.CommandPayload{RequestID: "req-send-fail", Command: "ls", Level: protocol.CapObserve})
	if err == nil {
		t.Fatal("expected send error")
	}
	if tracker.InFlight() != 0 {
		t.Fatalf("expected 0 inflight after send failure, got %d", tracker.InFlight())
	}
}

func TestWaitForResult_TimeoutCancelsPending(t *testing.T) {
	tracker := cmdtracker.New(time.Minute)
	svc := NewService(&stubSender{}, tracker)

	cmd := protocol.CommandPayload{RequestID: "req-timeout", Command: "ls", Level: protocol.CapObserve}
	pending, err := svc.DispatchTracked("probe-1", cmd)
	if err != nil {
		t.Fatalf("DispatchTracked returned error: %v", err)
	}

	_, err = svc.WaitForResult(context.Background(), cmd.RequestID, pending, 15*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if tracker.InFlight() != 0 {
		t.Fatalf("expected 0 inflight after timeout, got %d", tracker.InFlight())
	}
}

func TestResultText(t *testing.T) {
	if got := ResultText(&protocol.CommandResultPayload{ExitCode: 0, Stdout: " hello "}); got != "hello" {
		t.Fatalf("unexpected stdout mapping: %q", got)
	}
	if got := ResultText(&protocol.CommandResultPayload{ExitCode: 0, Stderr: " warn "}); got != "warn" {
		t.Fatalf("unexpected stderr mapping: %q", got)
	}
	if got := ResultText(&protocol.CommandResultPayload{ExitCode: 0}); got != "command completed with exit_code=0" {
		t.Fatalf("unexpected empty output mapping: %q", got)
	}
	if got := ResultText(&protocol.CommandResultPayload{ExitCode: 2, Stderr: "boom"}); got != "exit_code=2\nboom" {
		t.Fatalf("unexpected non-zero mapping: %q", got)
	}
}
