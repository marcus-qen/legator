package commanddispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// CommandInvokeInput is the shell-normalized invoke contract shared by HTTP
// and MCP command entrypoints.
type CommandInvokeInput struct {
	ProbeID string
	Command protocol.CommandPayload
	Policy  DispatchPolicy
	Surface ProjectionDispatchSurface
}

// CommandInvokeProjection is the renderer handoff envelope emitted by the
// shared command invoke seam.
type CommandInvokeProjection struct {
	Surface       ProjectionDispatchSurface
	RequestID     string
	WaitForResult bool
	Envelope      *CommandResultEnvelope
}

type dispatchWithPolicyInvoker interface {
	DispatchWithPolicy(ctx context.Context, probeID string, cmd protocol.CommandPayload, policy DispatchPolicy) *CommandResultEnvelope
}

// AssembleCommandInvokeHTTP normalizes HTTP dispatch shell input into the
// shared invoke contract while preserving request-id + policy behavior.
func AssembleCommandInvokeHTTP(probeID string, cmd protocol.CommandPayload, wantWait, wantStream bool) *CommandInvokeInput {
	if cmd.RequestID == "" {
		cmd.RequestID = NextCommandRequestID()
	}

	policy := DispatchOnlyPolicy(wantStream)
	if wantWait {
		timeout := 30 * time.Second
		if cmd.Timeout > 0 {
			timeout = cmd.Timeout + 5*time.Second
		}
		policy = WaitPolicy(timeout)
		policy.StreamOutput = wantStream
	}

	return &CommandInvokeInput{
		ProbeID: probeID,
		Command: cmd,
		Policy:  policy,
		Surface: ProjectionDispatchSurfaceHTTP,
	}
}

// AssembleCommandInvokeMCP normalizes MCP run-command input into the shared
// invoke contract while preserving request-id + wait policy behavior.
func AssembleCommandInvokeMCP(probeID, command string, level protocol.CapabilityLevel) *CommandInvokeInput {
	return &CommandInvokeInput{
		ProbeID: probeID,
		Command: protocol.CommandPayload{
			RequestID: NextCommandRequestID(),
			Command:   command,
			Level:     level,
		},
		Policy:  WaitPolicy(30 * time.Second),
		Surface: ProjectionDispatchSurfaceMCP,
	}
}

// NextCommandRequestID centralizes command request-id generation for command
// invoke adapters.
func NextCommandRequestID() string {
	return fmt.Sprintf("cmd-%d", time.Now().UnixNano()%100000)
}

// InvokeCommandForSurface dispatches via the shared invoke contract and returns
// the renderer-facing handoff projection.
func InvokeCommandForSurface(ctx context.Context, input *CommandInvokeInput, invoker dispatchWithPolicyInvoker) *CommandInvokeProjection {
	if input == nil {
		return nil
	}

	projection := &CommandInvokeProjection{
		Surface:       input.Surface,
		RequestID:     input.Command.RequestID,
		WaitForResult: input.Policy.WaitForResult,
	}
	if invoker == nil {
		return projection
	}

	projection.Envelope = invoker.DispatchWithPolicy(ctx, input.ProbeID, input.Command, input.Policy)
	return projection
}
