package commanddispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/protocol"
)

var (
	ErrTimeout     = errors.New("timeout waiting for probe response")
	ErrEmptyResult = errors.New("command failed: empty result")
)

type commandSender interface {
	SendTo(probeID string, msgType protocol.MessageType, payload any) error
}

type commandTracker interface {
	Track(requestID, probeID, command string, level protocol.CapabilityLevel) *cmdtracker.PendingCommand
	Cancel(requestID string)
}

// ResultState captures the normalized outcome for a dispatch attempt.
type ResultState string

const (
	ResultStateDispatched    ResultState = "dispatched"
	ResultStateCompleted     ResultState = "completed"
	ResultStateTimeout       ResultState = "timeout"
	ResultStateCanceled      ResultState = "canceled"
	ResultStateDispatchError ResultState = "dispatch_error"
	ResultStateResultError   ResultState = "result_error"
)

// DispatchPolicy controls command dispatch semantics used by callers.
type DispatchPolicy struct {
	WaitForResult       bool
	StreamOutput        bool
	Timeout             time.Duration
	CancelOnContextDone bool
}

// DispatchOnlyPolicy is fire-and-forget dispatch; optional stream flag is applied to cmd.Stream.
func DispatchOnlyPolicy(stream bool) DispatchPolicy {
	return DispatchPolicy{
		WaitForResult: false,
		StreamOutput:  stream,
	}
}

// WaitPolicy dispatches and waits for a final result with context-driven cancellation.
func WaitPolicy(timeout time.Duration) DispatchPolicy {
	return DispatchPolicy{
		WaitForResult:       true,
		Timeout:             timeout,
		CancelOnContextDone: true,
	}
}

func (p DispatchPolicy) normalized() DispatchPolicy {
	n := p
	if n.WaitForResult && n.Timeout <= 0 {
		n.Timeout = 30 * time.Second
	}
	return n
}

// HTTPErrorContract maps core dispatch outcomes to API-layer response details.
type HTTPErrorContract struct {
	Status        int
	Code          string
	Message       string
	SuppressWrite bool
}

// CommandResultEnvelope is the unified core command result contract.
// It intentionally carries the raw protocol result plus a normalized state and mapping helpers.
type CommandResultEnvelope struct {
	RequestID  string
	State      ResultState
	Dispatched bool
	Result     *protocol.CommandResultPayload
	Err        error
}

// HTTPError maps dispatch errors to API error responses without duplicating routing logic.
func (e *CommandResultEnvelope) HTTPError() (*HTTPErrorContract, bool) {
	if e == nil || e.Err == nil {
		return nil, false
	}

	switch {
	case errors.Is(e.Err, ErrTimeout):
		return &HTTPErrorContract{
			Status:  http.StatusGatewayTimeout,
			Code:    "timeout",
			Message: "timeout waiting for probe response",
		}, true
	case errors.Is(e.Err, context.Canceled), errors.Is(e.Err, context.DeadlineExceeded):
		return &HTTPErrorContract{SuppressWrite: true}, true
	default:
		return &HTTPErrorContract{
			Status:  http.StatusBadGateway,
			Code:    "bad_gateway",
			Message: e.Err.Error(),
		}, true
	}
}

// MCPError maps dispatch errors to MCP-facing error semantics.
func (e *CommandResultEnvelope) MCPError() error {
	if e == nil || e.Err == nil {
		return nil
	}
	if errors.Is(e.Err, ErrTimeout) || errors.Is(e.Err, context.Canceled) || errors.Is(e.Err, context.DeadlineExceeded) || errors.Is(e.Err, ErrEmptyResult) {
		return e.Err
	}
	return fmt.Errorf("dispatch command: %w", e.Err)
}

// Service orchestrates command dispatch tracking + wait semantics.
type Service struct {
	sender  commandSender
	tracker commandTracker
}

func NewService(sender commandSender, tracker commandTracker) *Service {
	return &Service{sender: sender, tracker: tracker}
}

func (s *Service) Dispatch(probeID string, cmd protocol.CommandPayload) error {
	return s.sender.SendTo(probeID, protocol.MsgCommand, cmd)
}

func (s *Service) DispatchTracked(probeID string, cmd protocol.CommandPayload) (*cmdtracker.PendingCommand, error) {
	pending := s.tracker.Track(cmd.RequestID, probeID, cmd.Command, cmd.Level)
	if err := s.sender.SendTo(probeID, protocol.MsgCommand, cmd); err != nil {
		s.tracker.Cancel(cmd.RequestID)
		return nil, err
	}
	return pending, nil
}

func (s *Service) waitForResult(ctx context.Context, requestID string, pending *cmdtracker.PendingCommand, policy DispatchPolicy) (*protocol.CommandResultPayload, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	timer := time.NewTimer(policy.Timeout)
	defer timer.Stop()

	select {
	case result, ok := <-pending.Result:
		if !ok {
			return nil, context.Canceled
		}
		return result, nil
	case <-timer.C:
		s.tracker.Cancel(requestID)
		return nil, ErrTimeout
	case <-ctx.Done():
		if policy.CancelOnContextDone {
			s.tracker.Cancel(requestID)
		}
		return nil, ctx.Err()
	}
}

// DispatchWithPolicy is the core dispatch entrypoint used across API/MCP/LLM callers.
func (s *Service) DispatchWithPolicy(ctx context.Context, probeID string, cmd protocol.CommandPayload, policy DispatchPolicy) *CommandResultEnvelope {
	policy = policy.normalized()
	cmd.Stream = cmd.Stream || policy.StreamOutput
	env := &CommandResultEnvelope{RequestID: cmd.RequestID}

	if !policy.WaitForResult {
		if err := s.Dispatch(probeID, cmd); err != nil {
			env.State = ResultStateDispatchError
			env.Err = err
			return env
		}
		env.State = ResultStateDispatched
		env.Dispatched = true
		return env
	}

	pending, err := s.DispatchTracked(probeID, cmd)
	if err != nil {
		env.State = ResultStateDispatchError
		env.Err = err
		return env
	}
	env.Dispatched = true

	result, err := s.waitForResult(ctx, cmd.RequestID, pending, policy)
	if err != nil {
		env.Err = err
		switch {
		case errors.Is(err, ErrTimeout):
			env.State = ResultStateTimeout
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			env.State = ResultStateCanceled
		default:
			env.State = ResultStateResultError
		}
		return env
	}
	if result == nil {
		env.State = ResultStateResultError
		env.Err = ErrEmptyResult
		return env
	}

	env.State = ResultStateCompleted
	env.Result = result
	return env
}

func (s *Service) WaitForResult(ctx context.Context, requestID string, pending *cmdtracker.PendingCommand, timeout time.Duration) (*protocol.CommandResultPayload, error) {
	policy := WaitPolicy(timeout)
	return s.waitForResult(ctx, requestID, pending, policy)
}

func (s *Service) DispatchAndWait(ctx context.Context, probeID string, cmd protocol.CommandPayload, timeout time.Duration) (*protocol.CommandResultPayload, error) {
	env := s.DispatchWithPolicy(ctx, probeID, cmd, WaitPolicy(timeout))
	if env == nil {
		return nil, ErrEmptyResult
	}
	return env.Result, env.Err
}

func ResultText(result *protocol.CommandResultPayload) string {
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		output = strings.TrimSpace(result.Stderr)
	}
	if output == "" {
		output = "command completed with exit_code=" + strconv.Itoa(result.ExitCode)
	}
	if result.ExitCode != 0 {
		output = "exit_code=" + strconv.Itoa(result.ExitCode) + "\n" + output
	}
	return output
}
