package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

const (
	unsupportedCommandInvokeScope   = transportwriter.UnsupportedSurfaceScopeCommandInvoke
	unsupportedCommandDispatchScope = transportwriter.UnsupportedSurfaceScopeCommandDispatch
)

var (
	unsupportedCommandInvokeSurfaceEnvelopeBuilder   = transportwriter.UnsupportedSurfaceEnvelopeBuilderForScope(unsupportedCommandInvokeScope)
	unsupportedCommandDispatchSurfaceEnvelopeBuilder = transportwriter.UnsupportedSurfaceEnvelopeBuilderForScope(unsupportedCommandDispatchScope)
)

func unsupportedCommandInvokeSurfaceMessage(surface string) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedCommandInvokeScope, surface)
}

func unsupportedCommandDispatchSurfaceMessage(surface ProjectionDispatchSurface) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedCommandDispatchScope, string(surface))
}

func unsupportedCommandInvokeSurfaceEnvelope(surface string) *transportwriter.ResponseEnvelope {
	return unsupportedCommandInvokeSurfaceEnvelopeBuilder(surface)
}

func unsupportedCommandDispatchSurfaceEnvelope(surface ProjectionDispatchSurface) *transportwriter.ResponseEnvelope {
	return unsupportedCommandDispatchSurfaceEnvelopeBuilder(string(surface))
}

func dispatchUnsupportedCommandDispatchSurfaceFallback(surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
	transportwriter.DispatchUnsupportedSurfaceFallback(
		surface,
		unsupportedCommandDispatchSurfaceEnvelope,
		writer,
		newCommandUnsupportedSurfaceFallbackWriter,
	)
}
