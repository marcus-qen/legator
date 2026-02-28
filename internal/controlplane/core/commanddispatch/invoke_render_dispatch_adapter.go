package commanddispatch

import (
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

// CommandInvokeRenderDispatchWriter exposes transport-specific write hooks while
// command invoke render-dispatch sequencing + fallback policy is centralized in core.
type CommandInvokeRenderDispatchWriter struct {
	WriteHTTPError      func(*HTTPErrorContract)
	WriteMCPError       func(error)
	WriteHTTPDispatched func(string)
	WriteHTTPResult     func(*protocol.CommandResultPayload)
	WriteMCPText        func(string)
}

type commandInvokeProjectionDispatchPolicy = projectiondispatch.Policy[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter]

var defaultCommandInvokeProjectionDispatchPolicyRegistry = projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]commandInvokeProjectionDispatchPolicy{
	ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter](dispatchCommandInvokeProjectionHTTP),
	ProjectionDispatchSurfaceMCP:  projectiondispatch.PolicyFunc[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter](dispatchCommandInvokeProjectionMCP),
})

// DispatchCommandInvokeProjection emits command invoke projection outputs for
// the target transport surface using centralized sequencing + fallback policy.
func DispatchCommandInvokeProjection(projection *CommandInvokeProjection, writer CommandInvokeRenderDispatchWriter) {
	if projection == nil {
		dispatchEmptyCommandInvokeProjection("", writer)
		return
	}

	projectiondispatch.DispatchResolvedOrUnsupported(
		projection.Surface,
		writer,
		ResolveCommandInvokeProjectionDispatchSurface,
		func(resolved ProjectionDispatchSurface, writer CommandInvokeRenderDispatchWriter) {
			projectiondispatch.DispatchForSurface(
				defaultCommandInvokeProjectionDispatchPolicyRegistry,
				resolved,
				projection,
				writer,
				dispatchUnsupportedCommandInvokeProjectionSurface,
			)
		},
		dispatchUnsupportedCommandInvokeProjectionSurface,
	)
}

func dispatchCommandInvokeProjectionHTTP(projection *CommandInvokeProjection, writer CommandInvokeRenderDispatchWriter) {
	if projection == nil || projection.Envelope == nil {
		dispatchEmptyCommandInvokeProjection(ProjectionDispatchSurfaceHTTP, writer)
		return
	}

	if DispatchCommandErrorsForSurface(projection.Envelope, ProjectionDispatchSurfaceHTTP, writer.projectionWriter()) {
		return
	}

	if !projection.WaitForResult {
		if writer.WriteHTTPDispatched != nil {
			writer.WriteHTTPDispatched(projection.RequestID)
		}
		return
	}

	DispatchCommandReadForSurface(projection.Envelope.Result, ProjectionDispatchSurfaceHTTP, writer.projectionWriter())
}

func dispatchCommandInvokeProjectionMCP(projection *CommandInvokeProjection, writer CommandInvokeRenderDispatchWriter) {
	if projection == nil || projection.Envelope == nil {
		dispatchEmptyCommandInvokeProjection(ProjectionDispatchSurfaceMCP, writer)
		return
	}

	if DispatchCommandErrorsForSurface(projection.Envelope, ProjectionDispatchSurfaceMCP, writer.projectionWriter()) {
		return
	}

	if projection.Envelope.Result == nil {
		dispatchEmptyCommandInvokeProjection(ProjectionDispatchSurfaceMCP, writer)
		return
	}

	DispatchCommandReadForSurface(projection.Envelope.Result, ProjectionDispatchSurfaceMCP, writer.projectionWriter())
}

func dispatchEmptyCommandInvokeProjection(surface ProjectionDispatchSurface, writer CommandInvokeRenderDispatchWriter) {
	if surface == "" {
		surface = inferCommandInvokeProjectionSurface(writer)
	}

	switch surface {
	case ProjectionDispatchSurfaceMCP:
		if writer.WriteMCPError != nil {
			writer.WriteMCPError(ErrEmptyResult)
		}
	case ProjectionDispatchSurfaceHTTP:
		if writer.WriteHTTPError != nil {
			writer.WriteHTTPError(&HTTPErrorContract{
				Status:  http.StatusBadGateway,
				Code:    "bad_gateway",
				Message: "command dispatch failed",
			})
		}
	default:
		dispatchUnsupportedCommandInvokeProjectionSurface(surface, writer)
	}
}

func inferCommandInvokeProjectionSurface(writer CommandInvokeRenderDispatchWriter) ProjectionDispatchSurface {
	hasHTTP := writer.WriteHTTPError != nil || writer.WriteHTTPDispatched != nil || writer.WriteHTTPResult != nil
	hasMCP := writer.WriteMCPError != nil || writer.WriteMCPText != nil

	switch {
	case hasHTTP && !hasMCP:
		return ProjectionDispatchSurfaceHTTP
	case hasMCP && !hasHTTP:
		return ProjectionDispatchSurfaceMCP
	case writer.WriteHTTPError != nil:
		return ProjectionDispatchSurfaceHTTP
	case writer.WriteMCPError != nil:
		return ProjectionDispatchSurfaceMCP
	default:
		return ""
	}
}

func dispatchUnsupportedCommandInvokeProjectionSurface(surface ProjectionDispatchSurface, writer CommandInvokeRenderDispatchWriter) {
	dispatchUnsupportedCommandDispatchSurfaceFallback(surface, writer.projectionWriter())
}

func (w CommandInvokeRenderDispatchWriter) projectionWriter() CommandProjectionDispatchWriter {
	return CommandProjectionDispatchWriter{
		WriteHTTPError:  w.WriteHTTPError,
		WriteMCPError:   w.WriteMCPError,
		WriteHTTPResult: w.WriteHTTPResult,
		WriteMCPText:    w.WriteMCPText,
	}
}
