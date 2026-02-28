package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

const unsupportedDecideApprovalScope = transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch

var unsupportedDecideApprovalSurfaceEnvelopeBuilder = transportwriter.UnsupportedSurfaceEnvelopeBuilderForScope(unsupportedDecideApprovalScope)

func unsupportedDecideApprovalSurfaceMessage[Surface ~string](surface Surface) string {
	return transportwriter.UnsupportedSurfaceMessageForSurface(unsupportedDecideApprovalScope, surface)
}

func unsupportedDecideApprovalSurfaceEnvelope[Surface ~string](surface Surface) *transportwriter.ResponseEnvelope {
	return transportwriter.BuildUnsupportedSurfaceEnvelope(unsupportedDecideApprovalSurfaceEnvelopeBuilder, surface)
}

func dispatchUnsupportedDecideApprovalSurfaceFallback[Surface ~string](surface Surface, writer DecideApprovalResponseDispatchWriter) {
	transportwriter.DispatchUnsupportedSurfaceFallback(
		surface,
		unsupportedDecideApprovalSurfaceEnvelope,
		writer,
		newDecideApprovalUnsupportedSurfaceFallbackWriter,
	)
}
