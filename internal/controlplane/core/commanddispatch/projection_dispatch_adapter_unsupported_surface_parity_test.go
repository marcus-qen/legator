package commanddispatch

import (
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

func TestDispatchCommandErrorsForSurface_UnsupportedSurfaceFallbackParityWithLegacyInlineBranch(t *testing.T) {
	tests := []struct {
		name     string
		withHTTP bool
		withMCP  bool
	}{
		{name: "http first when both callbacks present", withHTTP: true, withMCP: true},
		{name: "http only", withHTTP: true, withMCP: false},
		{name: "mcp fallback when http callback absent", withHTTP: false, withMCP: true},
		{name: "no callbacks", withHTTP: false, withMCP: false},
	}

	surface := ProjectionDispatchSurface("bogus")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandUnsupportedFallbackCapture{}
			legacyCapture := commandUnsupportedFallbackCapture{}

			newHandled := DispatchCommandErrorsForSurface(nil, surface, newCapture.writer(tt.withHTTP, tt.withMCP))
			legacyHandled := legacyDispatchCommandErrorsForSurfaceUnsupportedInlineBranch(nil, surface, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("unsupported-surface adapter parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
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

func legacyDispatchUnsupportedCommandSurfaceAdapterInlineBranch(surface ProjectionDispatchSurface, writer commandDispatchAdapterWriter) {
	dispatchUnsupportedCommandDispatchSurfaceFallback(surface, writer.writer)
	if writer.handled != nil {
		*writer.handled = true
	}
}
