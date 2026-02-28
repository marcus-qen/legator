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

func unsupportedCommandInvokeSurfaceMessage[Surface ~string](surface Surface) string {
	return transportwriter.UnsupportedSurfaceMessageForSurface(unsupportedCommandInvokeScope, surface)
}

func unsupportedCommandDispatchSurfaceMessage(surface ProjectionDispatchSurface) string {
	return transportwriter.UnsupportedSurfaceMessageForSurface(unsupportedCommandDispatchScope, surface)
}

func unsupportedCommandInvokeSurfaceEnvelope[Surface ~string](surface Surface) *transportwriter.ResponseEnvelope {
	return transportwriter.BuildUnsupportedSurfaceEnvelope(unsupportedCommandInvokeSurfaceEnvelopeBuilder, surface)
}

func unsupportedCommandDispatchSurfaceEnvelope(surface ProjectionDispatchSurface) *transportwriter.ResponseEnvelope {
	return transportwriter.BuildUnsupportedSurfaceEnvelope(unsupportedCommandDispatchSurfaceEnvelopeBuilder, surface)
}

func dispatchUnsupportedCommandDispatchSurfaceFallback(surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
	transportwriter.DispatchUnsupportedSurfaceFallback(
		surface,
		unsupportedCommandDispatchSurfaceEnvelope,
		writer,
		newCommandUnsupportedSurfaceFallbackWriter,
	)
}
