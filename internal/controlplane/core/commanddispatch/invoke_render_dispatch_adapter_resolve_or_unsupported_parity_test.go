package commanddispatch

import (
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestDispatchCommandInvokeProjection_ResolveOrUnsupportedBranchParityWithLegacyInlineBranch(t *testing.T) {
	tests := []struct {
		name       string
		projection *CommandInvokeProjection
		withHTTP   bool
		withMCP    bool
	}{
		{name: "nil projection", projection: nil, withHTTP: true, withMCP: true},
		{
			name: "http resolved",
			projection: &CommandInvokeProjection{
				Surface:   ProjectionDispatchSurfaceHTTP,
				RequestID: "req-http",
				Envelope:  &CommandResultEnvelope{Dispatched: true},
			},
			withHTTP: true,
			withMCP:  true,
		},
		{
			name: "mcp resolved",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceMCP,
				RequestID:     "req-mcp",
				WaitForResult: true,
				Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{
					ExitCode: 0,
					Stdout:   "ok",
				}},
			},
			withHTTP: true,
			withMCP:  true,
		},
		{
			name: "unsupported with both callbacks",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurface("bogus"),
				RequestID:     "req-unsupported",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{},
			},
			withHTTP: true,
			withMCP:  true,
		},
		{
			name: "unsupported with mcp callback",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurface("bogus"),
				RequestID:     "req-unsupported-mcp",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{},
			},
			withHTTP: false,
			withMCP:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandInvokeBranchCapture{}
			legacyCapture := commandInvokeBranchCapture{}

			DispatchCommandInvokeProjection(tt.projection, newCapture.writer(tt.withHTTP, tt.withMCP))
			legacyDispatchCommandInvokeProjectionResolveUnsupportedInlineBranch(tt.projection, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if !newCapture.equal(legacyCapture) {
				t.Fatalf("resolve-or-unsupported branch parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

func legacyDispatchCommandInvokeProjectionResolveUnsupportedInlineBranch(projection *CommandInvokeProjection, writer CommandInvokeRenderDispatchWriter) {
	if projection == nil {
		dispatchEmptyCommandInvokeProjection("", writer)
		return
	}

	resolved, ok := ResolveCommandInvokeProjectionDispatchSurface(projection.Surface)
	if !ok {
		dispatchUnsupportedCommandInvokeProjectionSurface(projection.Surface, writer)
		return
	}

	projectiondispatch.DispatchForSurface(
		defaultCommandInvokeProjectionDispatchPolicyRegistry,
		resolved,
		projection,
		writer,
		dispatchUnsupportedCommandInvokeProjectionSurface,
	)
}

type commandInvokeBranchCapture struct {
	httpErrCalled    bool
	httpErr          *HTTPErrorContract
	mcpErrCalled     bool
	mcpErr           error
	dispatchedCalled bool
	dispatched       string
	resultCalled     bool
	result           *protocol.CommandResultPayload
	textCalled       bool
	text             string
}

func (c *commandInvokeBranchCapture) writer(withHTTP, withMCP bool) CommandInvokeRenderDispatchWriter {
	writer := CommandInvokeRenderDispatchWriter{}
	if withHTTP {
		writer.WriteHTTPError = func(err *HTTPErrorContract) {
			c.httpErrCalled = true
			c.httpErr = err
		}
		writer.WriteHTTPDispatched = func(requestID string) {
			c.dispatchedCalled = true
			c.dispatched = requestID
		}
		writer.WriteHTTPResult = func(result *protocol.CommandResultPayload) {
			c.resultCalled = true
			c.result = result
		}
	}
	if withMCP {
		writer.WriteMCPError = func(err error) {
			c.mcpErrCalled = true
			c.mcpErr = err
		}
		writer.WriteMCPText = func(text string) {
			c.textCalled = true
			c.text = text
		}
	}
	return writer
}

func (c commandInvokeBranchCapture) equal(other commandInvokeBranchCapture) bool {
	if c.httpErrCalled != other.httpErrCalled || !reflect.DeepEqual(c.httpErr, other.httpErr) {
		return false
	}
	if c.mcpErrCalled != other.mcpErrCalled {
		return false
	}
	switch {
	case c.mcpErr == nil && other.mcpErr == nil:
	case c.mcpErr == nil || other.mcpErr == nil:
		return false
	default:
		if c.mcpErr.Error() != other.mcpErr.Error() {
			return false
		}
	}
	if c.dispatchedCalled != other.dispatchedCalled || c.dispatched != other.dispatched {
		return false
	}
	if c.resultCalled != other.resultCalled || !reflect.DeepEqual(c.result, other.result) {
		return false
	}
	if c.textCalled != other.textCalled || c.text != other.text {
		return false
	}
	return true
}
