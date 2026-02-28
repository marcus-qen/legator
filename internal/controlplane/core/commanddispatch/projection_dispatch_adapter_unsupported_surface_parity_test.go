package commanddispatch

import (
	"errors"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestDispatchCommandErrorsForSurface_ResolveOrUnsupportedBranchParityWithLegacyInlineBranch(t *testing.T) {
	tests := []struct {
		name     string
		surface  ProjectionDispatchSurface
		withHTTP bool
		withMCP  bool
		envelope *CommandResultEnvelope
	}{
		{name: "http resolved", surface: ProjectionDispatchSurfaceHTTP, withHTTP: true, withMCP: true, envelope: &CommandResultEnvelope{Err: errors.New("not connected")}},
		{name: "mcp resolved", surface: ProjectionDispatchSurfaceMCP, withHTTP: true, withMCP: true, envelope: &CommandResultEnvelope{Err: errors.New("not connected")}},
		{name: "http resolved no error", surface: ProjectionDispatchSurfaceHTTP, withHTTP: true, withMCP: true, envelope: &CommandResultEnvelope{}},
		{name: "unsupported with both callbacks", surface: ProjectionDispatchSurface("bogus"), withHTTP: true, withMCP: true, envelope: nil},
		{name: "unsupported with mcp callback", surface: ProjectionDispatchSurface("bogus"), withHTTP: false, withMCP: true, envelope: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandUnsupportedFallbackCapture{}
			legacyCapture := commandUnsupportedFallbackCapture{}

			newHandled := DispatchCommandErrorsForSurface(tt.envelope, tt.surface, newCapture.writer(tt.withHTTP, tt.withMCP))
			legacyHandled := legacyDispatchCommandErrorsForSurfaceUnsupportedInlineBranch(tt.envelope, tt.surface, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("resolve-or-unsupported branch parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

func TestDispatchCommandReadForSurface_ResolveOrUnsupportedBranchParityWithLegacyInlineBranch(t *testing.T) {
	tests := []struct {
		name     string
		surface  ProjectionDispatchSurface
		withHTTP bool
		withMCP  bool
	}{
		{name: "http resolved", surface: ProjectionDispatchSurfaceHTTP, withHTTP: true, withMCP: true},
		{name: "mcp resolved", surface: ProjectionDispatchSurfaceMCP, withHTTP: true, withMCP: true},
		{name: "unsupported with both callbacks", surface: ProjectionDispatchSurface("bogus"), withHTTP: true, withMCP: true},
		{name: "unsupported with mcp callback", surface: ProjectionDispatchSurface("bogus"), withHTTP: false, withMCP: true},
	}

	result := &protocol.CommandResultPayload{ExitCode: 2, Stdout: "ok", Stderr: "boom"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandReadBranchCapture{}
			legacyCapture := commandReadBranchCapture{}

			DispatchCommandReadForSurface(result, tt.surface, newCapture.writer(tt.withHTTP, tt.withMCP))
			legacyDispatchCommandReadForSurfaceResolveUnsupportedInlineBranch(result, tt.surface, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if !newCapture.equal(legacyCapture) {
				t.Fatalf("resolve-or-unsupported branch parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

func TestDispatchUnsupportedCommandSurfaceAdapter_HandledWiringParityWithLegacyInlineBranch(t *testing.T) {
	tests := []struct {
		name           string
		withHTTP       bool
		withMCP        bool
		withHandledPtr bool
		handledBefore  bool
	}{
		{name: "handled pointer false before", withHTTP: true, withMCP: true, withHandledPtr: true, handledBefore: false},
		{name: "handled pointer true before", withHTTP: true, withMCP: false, withHandledPtr: true, handledBefore: true},
		{name: "handled pointer absent", withHTTP: false, withMCP: true, withHandledPtr: false, handledBefore: false},
	}

	surface := ProjectionDispatchSurface("bogus")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandUnsupportedFallbackCapture{}
			legacyCapture := commandUnsupportedFallbackCapture{}

			newHandled := tt.handledBefore
			legacyHandled := tt.handledBefore

			newWriter := commandDispatchAdapterWriter{writer: newCapture.writer(tt.withHTTP, tt.withMCP)}
			legacyWriter := commandDispatchAdapterWriter{writer: legacyCapture.writer(tt.withHTTP, tt.withMCP)}
			if tt.withHandledPtr {
				newWriter.handled = &newHandled
				legacyWriter.handled = &legacyHandled
			}

			dispatchUnsupportedCommandSurfaceAdapter(surface, newWriter)
			legacyDispatchUnsupportedCommandSurfaceAdapterInlineBranch(surface, legacyWriter)

			if newHandled != legacyHandled {
				t.Fatalf("handled wiring parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("capture parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

func legacyDispatchCommandErrorsForSurfaceUnsupportedInlineBranch(envelope *CommandResultEnvelope, surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) bool {
	resolved, ok := ResolveCommandDispatchProjectionSurface(surface)
	if !ok {
		dispatchUnsupportedCommandDispatchSurfaceFallback(surface, writer)
		return true
	}

	handled := false
	projectiondispatch.DispatchForSurface(
		defaultCommandDispatchErrorPolicyRegistry,
		resolved,
		envelope,
		commandDispatchAdapterWriter{writer: writer, handled: &handled},
		legacyDispatchUnsupportedCommandSurfaceAdapterInlineBranch,
	)
	return handled
}

func legacyDispatchCommandReadForSurfaceResolveUnsupportedInlineBranch(result *protocol.CommandResultPayload, surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
	resolved, ok := ResolveCommandReadProjectionSurface(surface)
	if !ok {
		dispatchUnsupportedCommandDispatchSurfaceFallback(surface, writer)
		return
	}

	projectiondispatch.DispatchForSurface(
		defaultCommandReadPolicyRegistry,
		resolved,
		result,
		commandDispatchAdapterWriter{writer: writer, handled: nil},
		legacyDispatchUnsupportedCommandSurfaceAdapterInlineBranch,
	)
}

func legacyDispatchUnsupportedCommandSurfaceAdapterInlineBranch(surface ProjectionDispatchSurface, writer commandDispatchAdapterWriter) {
	dispatchUnsupportedCommandDispatchSurfaceFallback(surface, writer.writer)
	if writer.handled != nil {
		*writer.handled = true
	}
}

type commandReadBranchCapture struct {
	httpErrCalled bool
	httpErr       *HTTPErrorContract
	mcpErrCalled  bool
	mcpErr        error
	resultCalled  bool
	result        *protocol.CommandResultPayload
	textCalled    bool
	text          string
}

func (c *commandReadBranchCapture) writer(withHTTP, withMCP bool) CommandProjectionDispatchWriter {
	writer := CommandProjectionDispatchWriter{}
	if withHTTP {
		writer.WriteHTTPError = func(err *HTTPErrorContract) {
			c.httpErrCalled = true
			c.httpErr = err
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

func (c commandReadBranchCapture) equal(other commandReadBranchCapture) bool {
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
	if c.resultCalled != other.resultCalled || !reflect.DeepEqual(c.result, other.result) {
		return false
	}
	if c.textCalled != other.textCalled || c.text != other.text {
		return false
	}
	return true
}
