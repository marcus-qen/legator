package commanddispatch

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/protocol"
)

var ErrTimeout = errors.New("timeout waiting for probe response")

type commandSender interface {
	SendTo(probeID string, msgType protocol.MessageType, payload any) error
}

type commandTracker interface {
	Track(requestID, probeID, command string, level protocol.CapabilityLevel) *cmdtracker.PendingCommand
	Cancel(requestID string)
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

func (s *Service) WaitForResult(ctx context.Context, requestID string, pending *cmdtracker.PendingCommand, timeout time.Duration) (*protocol.CommandResultPayload, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result := <-pending.Result:
		return result, nil
	case <-timer.C:
		s.tracker.Cancel(requestID)
		return nil, ErrTimeout
	case <-ctx.Done():
		s.tracker.Cancel(requestID)
		return nil, ctx.Err()
	}
}

func (s *Service) DispatchAndWait(ctx context.Context, probeID string, cmd protocol.CommandPayload, timeout time.Duration) (*protocol.CommandResultPayload, error) {
	pending, err := s.DispatchTracked(probeID, cmd)
	if err != nil {
		return nil, err
	}
	return s.WaitForResult(ctx, cmd.RequestID, pending, timeout)
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
