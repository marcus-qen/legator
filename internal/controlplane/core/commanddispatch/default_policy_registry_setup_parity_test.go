package commanddispatch

import (
	"errors"
	"net/http"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestDefaultCommandDispatchErrorPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	legacyRegistry := newCommandDispatchErrorPolicyRegistry(map[ProjectionDispatchSurface]commandDispatchErrorPolicy{
		ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*CommandResultEnvelope, commandDispatchAdapterWriter](dispatchCommandEnvelopeHTTPError),
		ProjectionDispatchSurfaceMCP:  projectiondispatch.PolicyFunc[*CommandResultEnvelope, commandDispatchAdapterWriter](dispatchCommandEnvelopeMCPError),
	})

	tests := []struct {
		name     string
		surface  ProjectionDispatchSurface
		envelope *CommandResultEnvelope
	}{
		{name: "http hit", surface: ProjectionDispatchSurfaceHTTP, envelope: &CommandResultEnvelope{Err: ErrTimeout}},
		{name: "mcp hit", surface: ProjectionDispatchSurfaceMCP, envelope: &CommandResultEnvelope{Err: errors.New("probe disconnected")}},
		{name: "resolver miss unsupported fallback", surface: ProjectionDispatchSurface("bogus"), envelope: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := dispatchCommandErrorsForSurfaceWithRegistryCapture(defaultCommandDispatchErrorPolicyRegistry, tt.envelope, tt.surface)
			legacyCapture := dispatchCommandErrorsForSurfaceWithRegistryCapture(legacyRegistry, tt.envelope, tt.surface)
			if newCapture != legacyCapture {
				t.Fatalf("default dispatch-error registry parity mismatch for %q: new=%+v legacy=%+v", tt.surface, newCapture, legacyCapture)
			}
		})
	}
}

func TestDefaultCommandReadPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	legacyRegistry := newCommandReadPolicyRegistry(map[ProjectionDispatchSurface]commandReadPolicy{
		ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*protocol.CommandResultPayload, commandDispatchAdapterWriter](dispatchCommandReadHTTP),
		ProjectionDispatchSurfaceMCP:  projectiondispatch.PolicyFunc[*protocol.CommandResultPayload, commandDispatchAdapterWriter](dispatchCommandReadMCP),
	})

	result := &protocol.CommandResultPayload{ExitCode: 2, Stderr: " boom "}
	tests := []struct {
		name    string
		surface ProjectionDispatchSurface
		result  *protocol.CommandResultPayload
	}{
		{name: "http hit", surface: ProjectionDispatchSurfaceHTTP, result: result},
		{name: "mcp hit", surface: ProjectionDispatchSurfaceMCP, result: result},
		{name: "resolver miss unsupported fallback", surface: ProjectionDispatchSurface("bogus"), result: result},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := dispatchCommandReadForSurfaceWithRegistryCapture(defaultCommandReadPolicyRegistry, tt.result, tt.surface)
			legacyCapture := dispatchCommandReadForSurfaceWithRegistryCapture(legacyRegistry, tt.result, tt.surface)
			if newCapture != legacyCapture {
				t.Fatalf("default read-policy registry parity mismatch for %q: new=%+v legacy=%+v", tt.surface, newCapture, legacyCapture)
			}
		})
	}
}

func TestDefaultCommandInvokeProjectionDispatchPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	legacyRegistry := newCommandInvokeProjectionDispatchPolicyRegistry(map[ProjectionDispatchSurface]commandInvokeProjectionDispatchPolicy{
		ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter](dispatchCommandInvokeProjectionHTTP),
		ProjectionDispatchSurfaceMCP:  projectiondispatch.PolicyFunc[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter](dispatchCommandInvokeProjectionMCP),
	})

	tests := []struct {
		name       string
		projection *CommandInvokeProjection
	}{
		{
			name: "http hit",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceHTTP,
				RequestID:     "req-http",
				WaitForResult: false,
				Envelope:      &CommandResultEnvelope{},
			},
		},
		{
			name: "mcp hit",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceMCP,
				WaitForResult: true,
				Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{
					ExitCode: 1,
					Stderr:   " boom ",
				}},
			},
		},
		{
			name: "resolver miss unsupported fallback",
			projection: &CommandInvokeProjection{
				Surface:  ProjectionDispatchSurface("bogus"),
				Envelope: &CommandResultEnvelope{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := dispatchCommandInvokeProjectionWithRegistryCapture(defaultCommandInvokeProjectionDispatchPolicyRegistry, tt.projection)
			legacyCapture := dispatchCommandInvokeProjectionWithRegistryCapture(legacyRegistry, tt.projection)
			if newCapture != legacyCapture {
				t.Fatalf("default invoke-policy registry parity mismatch for %q: new=%+v legacy=%+v", tt.projection.Surface, newCapture, legacyCapture)
			}
		})
	}
}

type commandDefaultPolicyRegistryCapture struct {
	handled      bool
	httpErrCalls int
	httpStatus   int
	httpCode     string
	httpMessage  string
	mcpErrCalls  int
	mcpErrText   string
	httpResult   bool
	httpExitCode int
	httpStderr   string
	httpDispatch string
	mcpText      string
}

func dispatchCommandErrorsForSurfaceWithRegistryCapture(
	registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandDispatchErrorPolicy],
	envelope *CommandResultEnvelope,
	surface ProjectionDispatchSurface,
) commandDefaultPolicyRegistryCapture {
	capture := commandDefaultPolicyRegistryCapture{}
	writer := CommandProjectionDispatchWriter{
		WriteHTTPError: func(contract *HTTPErrorContract) {
			capture.httpErrCalls++
			if contract != nil {
				capture.httpStatus = contract.Status
				capture.httpCode = contract.Code
				capture.httpMessage = contract.Message
			}
		},
		WriteMCPError: func(err error) {
			capture.mcpErrCalls++
			if err != nil {
				capture.mcpErrText = err.Error()
			}
		},
	}

	capture.handled = dispatchCommandErrorsForSurfaceWithRegistry(envelope, surface, writer, registry)
	return capture
}

func dispatchCommandErrorsForSurfaceWithRegistry(
	envelope *CommandResultEnvelope,
	surface ProjectionDispatchSurface,
	writer CommandProjectionDispatchWriter,
	registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandDispatchErrorPolicy],
) bool {
	handled := false
	adapterWriter := commandDispatchAdapterWriter{writer: writer, handled: &handled}

	projectiondispatch.DispatchResolvedPolicyForSurface(
		surface,
		envelope,
		adapterWriter,
		ResolveCommandDispatchProjectionSurface,
		registry,
		dispatchUnsupportedCommandSurfaceAdapter,
	)
	return handled
}

func dispatchCommandReadForSurfaceWithRegistryCapture(
	registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandReadPolicy],
	result *protocol.CommandResultPayload,
	surface ProjectionDispatchSurface,
) commandDefaultPolicyRegistryCapture {
	capture := commandDefaultPolicyRegistryCapture{}

	dispatchCommandReadForSurfaceWithRegistry(result, surface, CommandProjectionDispatchWriter{
		WriteHTTPError: func(contract *HTTPErrorContract) {
			capture.httpErrCalls++
			if contract != nil {
				capture.httpStatus = contract.Status
				capture.httpCode = contract.Code
				capture.httpMessage = contract.Message
			}
		},
		WriteMCPError: func(err error) {
			capture.mcpErrCalls++
			if err != nil {
				capture.mcpErrText = err.Error()
			}
		},
		WriteHTTPResult: func(payload *protocol.CommandResultPayload) {
			capture.httpResult = payload != nil
			if payload != nil {
				capture.httpExitCode = payload.ExitCode
				capture.httpStderr = payload.Stderr
			}
		},
		WriteMCPText: func(text string) {
			capture.mcpText = text
		},
	}, registry)

	return capture
}

func dispatchCommandReadForSurfaceWithRegistry(
	result *protocol.CommandResultPayload,
	surface ProjectionDispatchSurface,
	writer CommandProjectionDispatchWriter,
	registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandReadPolicy],
) {
	projectiondispatch.DispatchResolvedPolicyForSurface(
		surface,
		result,
		commandDispatchAdapterWriter{writer: writer, handled: nil},
		ResolveCommandReadProjectionSurface,
		registry,
		dispatchUnsupportedCommandSurfaceAdapter,
	)
}

func dispatchCommandInvokeProjectionWithRegistryCapture(
	registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandInvokeProjectionDispatchPolicy],
	projection *CommandInvokeProjection,
) commandDefaultPolicyRegistryCapture {
	capture := commandDefaultPolicyRegistryCapture{}
	writer := CommandInvokeRenderDispatchWriter{
		WriteHTTPError: func(contract *HTTPErrorContract) {
			capture.httpErrCalls++
			if contract != nil {
				capture.httpStatus = contract.Status
				capture.httpCode = contract.Code
				capture.httpMessage = contract.Message
			}
		},
		WriteMCPError: func(err error) {
			capture.mcpErrCalls++
			if err != nil {
				capture.mcpErrText = err.Error()
			}
		},
		WriteHTTPDispatched: func(id string) {
			capture.httpDispatch = id
		},
		WriteHTTPResult: func(payload *protocol.CommandResultPayload) {
			capture.httpResult = payload != nil
			if payload != nil {
				capture.httpExitCode = payload.ExitCode
				capture.httpStderr = payload.Stderr
			}
		},
		WriteMCPText: func(text string) {
			capture.mcpText = text
		},
	}

	dispatchCommandInvokeProjectionWithRegistry(projection, writer, registry)
	return capture
}

func dispatchCommandInvokeProjectionWithRegistry(
	projection *CommandInvokeProjection,
	writer CommandInvokeRenderDispatchWriter,
	registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandInvokeProjectionDispatchPolicy],
) {
	if projection == nil {
		dispatchEmptyCommandInvokeProjection("", writer)
		return
	}

	projectiondispatch.DispatchResolvedPolicyForSurface(
		projection.Surface,
		projection,
		writer,
		ResolveCommandInvokeProjectionDispatchSurface,
		registry,
		dispatchUnsupportedCommandInvokeProjectionSurface,
	)
}

func TestDefaultPolicyRegistryParity_UnsupportedSurfaceHTTPFirstFallbackLock(t *testing.T) {
	projection := &CommandInvokeProjection{Surface: ProjectionDispatchSurface("bogus"), Envelope: &CommandResultEnvelope{}}
	capture := dispatchCommandInvokeProjectionWithRegistryCapture(defaultCommandInvokeProjectionDispatchPolicyRegistry, projection)
	if capture.httpErrCalls != 1 || capture.mcpErrCalls != 0 {
		t.Fatalf("fallback precedence mismatch: http=%d mcp=%d", capture.httpErrCalls, capture.mcpErrCalls)
	}
	if capture.httpStatus != http.StatusInternalServerError || capture.httpCode != "internal_error" {
		t.Fatalf("unexpected unsupported-surface HTTP contract: %+v", capture)
	}
}
