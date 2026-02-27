package commanddispatch

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

// CommandProjectionDispatchWriter exposes transport-specific write hooks while
// command projection/error selection is centralized in core.
type CommandProjectionDispatchWriter struct {
	WriteHTTPError  func(*HTTPErrorContract)
	WriteMCPError   func(error)
	WriteHTTPResult func(*protocol.CommandResultPayload)
	WriteMCPText    func(string)
}

type commandDispatchErrorPolicy = projectiondispatch.Policy[*CommandResultEnvelope, commandDispatchAdapterWriter]
type commandReadPolicy = projectiondispatch.Policy[*protocol.CommandResultPayload, commandDispatchAdapterWriter]

type commandDispatchAdapterWriter struct {
	writer  CommandProjectionDispatchWriter
	handled *bool
}

var defaultCommandDispatchErrorPolicyRegistry = projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]commandDispatchErrorPolicy{
	ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*CommandResultEnvelope, commandDispatchAdapterWriter](dispatchCommandEnvelopeHTTPError),
	ProjectionDispatchSurfaceMCP:  projectiondispatch.PolicyFunc[*CommandResultEnvelope, commandDispatchAdapterWriter](dispatchCommandEnvelopeMCPError),
})

var defaultCommandReadPolicyRegistry = projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]commandReadPolicy{
	ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*protocol.CommandResultPayload, commandDispatchAdapterWriter](dispatchCommandReadHTTP),
	ProjectionDispatchSurfaceMCP:  projectiondispatch.PolicyFunc[*protocol.CommandResultPayload, commandDispatchAdapterWriter](dispatchCommandReadMCP),
})

// DispatchCommandErrorsForSurface emits transport-specific errors from the
// command dispatch envelope. It returns true when an error was emitted.
func DispatchCommandErrorsForSurface(envelope *CommandResultEnvelope, surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) bool {
	resolved, ok := ResolveCommandDispatchProjectionSurface(surface)
	if !ok {
		dispatchUnsupportedCommandSurface(surface, writer)
		return true
	}

	handled := false
	projectiondispatch.DispatchForSurface(
		defaultCommandDispatchErrorPolicyRegistry,
		resolved,
		envelope,
		commandDispatchAdapterWriter{writer: writer, handled: &handled},
		dispatchUnsupportedCommandSurfaceAdapter,
	)
	return handled
}

// DispatchCommandReadForSurface emits transport-specific command-read outputs
// from the shared command result payload.
func DispatchCommandReadForSurface(result *protocol.CommandResultPayload, surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
	resolved, ok := ResolveCommandReadProjectionSurface(surface)
	if !ok {
		dispatchUnsupportedCommandSurface(surface, writer)
		return
	}

	projectiondispatch.DispatchForSurface(
		defaultCommandReadPolicyRegistry,
		resolved,
		result,
		commandDispatchAdapterWriter{writer: writer, handled: nil},
		dispatchUnsupportedCommandSurfaceAdapter,
	)
}

func dispatchCommandEnvelopeHTTPError(envelope *CommandResultEnvelope, writer commandDispatchAdapterWriter) {
	if envelope == nil {
		return
	}
	httpErr, ok := envelope.HTTPError()
	if !ok {
		return
	}
	if writer.handled != nil {
		*writer.handled = true
	}
	if writer.writer.WriteHTTPError != nil {
		writer.writer.WriteHTTPError(httpErr)
	}
}

func dispatchCommandEnvelopeMCPError(envelope *CommandResultEnvelope, writer commandDispatchAdapterWriter) {
	if envelope == nil {
		return
	}
	err := envelope.MCPError()
	if err == nil {
		return
	}
	if writer.handled != nil {
		*writer.handled = true
	}
	if writer.writer.WriteMCPError != nil {
		writer.writer.WriteMCPError(err)
	}
}

func dispatchCommandReadHTTP(result *protocol.CommandResultPayload, writer commandDispatchAdapterWriter) {
	if writer.writer.WriteHTTPResult != nil {
		writer.writer.WriteHTTPResult(result)
	}
}

func dispatchCommandReadMCP(result *protocol.CommandResultPayload, writer commandDispatchAdapterWriter) {
	if writer.writer.WriteMCPText != nil {
		writer.writer.WriteMCPText(ResultText(result))
	}
}

func dispatchUnsupportedCommandSurfaceAdapter(surface ProjectionDispatchSurface, writer commandDispatchAdapterWriter) {
	dispatchUnsupportedCommandSurface(surface, writer.writer)
	if writer.handled != nil {
		*writer.handled = true
	}
}

func dispatchUnsupportedCommandSurface(surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
	httpErr := &HTTPErrorContract{
		Status:  http.StatusInternalServerError,
		Code:    "internal_error",
		Message: fmt.Sprintf("unsupported command dispatch surface %q", string(surface)),
	}
	if writer.WriteHTTPError != nil {
		writer.WriteHTTPError(httpErr)
		return
	}
	if writer.WriteMCPError != nil {
		writer.WriteMCPError(errors.New(httpErr.Message))
	}
}
