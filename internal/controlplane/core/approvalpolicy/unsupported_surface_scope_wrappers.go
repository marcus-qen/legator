package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

const unsupportedDecideApprovalScope = transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch

var unsupportedDecideApprovalSurfaceEnvelopeBuilder = transportwriter.UnsupportedSurfaceEnvelopeBuilderForScope(unsupportedDecideApprovalScope)

func unsupportedDecideApprovalSurfaceMessage(surface string) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedDecideApprovalScope, surface)
}

func unsupportedDecideApprovalSurfaceEnvelope(surface string) *transportwriter.ResponseEnvelope {
	return unsupportedDecideApprovalSurfaceEnvelopeBuilder(surface)
}

func dispatchUnsupportedDecideApprovalSurfaceFallback(surface string, writer DecideApprovalResponseDispatchWriter) {
	transportwriter.DispatchUnsupportedSurfaceFallback(
		surface,
		unsupportedDecideApprovalSurfaceEnvelope,
		writer,
		newDecideApprovalUnsupportedSurfaceFallbackWriter,
	)
}
