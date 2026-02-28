package approvalpolicy

import (
	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

// DecideApprovalResponseDispatchWriter provides transport writers used by
// surface shells while emission policy is selected centrally in core.
type DecideApprovalResponseDispatchWriter struct {
	WriteSuccess   func(*DecideApprovalSuccess)
	WriteHTTPError func(*HTTPErrorContract)
	WriteMCPError  func(error)
}

// DispatchDecideApprovalResponseForSurface dispatches the shared decide
// projection to transport writers using centralized surface normalization.
func DispatchDecideApprovalResponseForSurface(projection *DecideApprovalProjection, surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
	builder := DecideApprovalResponseEnvelopeBuilder{Projection: projection}
	projectiondispatch.DispatchResolvedOrUnsupported(
		surface,
		writer,
		ResolveDecideApprovalTransportSurface,
		func(transportSurface transportwriter.Surface, writer DecideApprovalResponseDispatchWriter) {
			transportwriter.WriteFromBuilder(transportSurface, builder, newDecideApprovalWriterKernel(writer))
		},
		dispatchUnsupportedDecideApprovalSurfaceAdapterFallback,
	)
}

func dispatchUnsupportedDecideApprovalSurfaceAdapterFallback(surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
	projectiondispatch.DispatchUnsupportedSurfaceAdapterFallback(
		surface,
		writer,
		dispatchUnsupportedDecideApprovalSurfaceFallback,
		nil,
	)
}
