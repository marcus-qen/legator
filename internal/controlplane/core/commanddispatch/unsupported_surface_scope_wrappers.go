package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

const (
	unsupportedCommandInvokeScope   = transportwriter.UnsupportedSurfaceScopeCommandInvoke
	unsupportedCommandDispatchScope = transportwriter.UnsupportedSurfaceScopeCommandDispatch
)

func unsupportedCommandInvokeSurfaceMessage(surface string) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedCommandInvokeScope, surface)
}

func unsupportedCommandDispatchSurfaceMessage(surface ProjectionDispatchSurface) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedCommandDispatchScope, string(surface))
}

func unsupportedCommandInvokeSurfaceEnvelope(surface string) *transportwriter.ResponseEnvelope {
	return transportwriter.UnsupportedSurfaceEnvelope(unsupportedCommandInvokeSurfaceMessage(surface))
}

func unsupportedCommandDispatchSurfaceEnvelope(surface ProjectionDispatchSurface) *transportwriter.ResponseEnvelope {
	return transportwriter.UnsupportedSurfaceEnvelope(unsupportedCommandDispatchSurfaceMessage(surface))
}

func dispatchUnsupportedCommandDispatchSurfaceFallback(surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
	fallbackWriter := transportwriter.UnsupportedSurfaceFallbackWriter{WriteMCPError: writer.WriteMCPError}
	if writer.WriteHTTPError != nil {
		fallbackWriter.WriteHTTPError = func(err *transportwriter.HTTPError) {
			if err == nil {
				return
			}
			writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
		}
	}
	transportwriter.WriteUnsupportedSurfaceFallback(unsupportedCommandDispatchSurfaceEnvelope(surface), fallbackWriter)
}
