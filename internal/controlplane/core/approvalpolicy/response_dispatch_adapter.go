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

type decideApprovalResponseDispatchPolicy = projectiondispatch.Policy[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter]

var defaultDecideApprovalResponseDispatchPolicyRegistry = projectiondispatch.NewPolicyRegistry(map[DecideApprovalRenderSurface]decideApprovalResponseDispatchPolicy{
	DecideApprovalRenderSurfaceHTTP: projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](dispatchDecideApprovalResponseHTTP),
	DecideApprovalRenderSurfaceMCP:  projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](dispatchDecideApprovalResponseMCP),
})

// DispatchDecideApprovalResponseForSurface dispatches the shared decide
// projection to transport writers using centralized surface normalization.
func DispatchDecideApprovalResponseForSurface(projection *DecideApprovalProjection, surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
	projectiondispatch.DispatchResolvedPolicyForSurface(
		surface,
		projection,
		writer,
		resolveDecideApprovalResponseDispatchSurface,
		defaultDecideApprovalResponseDispatchPolicyRegistry,
		dispatchUnsupportedDecideApprovalSurfaceAdapterFallback,
	)
}

func resolveDecideApprovalResponseDispatchSurface(surface DecideApprovalRenderSurface) (DecideApprovalRenderSurface, bool) {
	if _, ok := ResolveDecideApprovalTransportSurface(surface); !ok {
		return "", false
	}
	return surface, true
}

func dispatchDecideApprovalResponseHTTP(projection *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
	transportwriter.WriteFromBuilder(
		transportwriter.SurfaceHTTP,
		DecideApprovalResponseEnvelopeBuilder{Projection: projection},
		newDecideApprovalWriterKernel(writer),
	)
}

func dispatchDecideApprovalResponseMCP(projection *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
	transportwriter.WriteFromBuilder(
		transportwriter.SurfaceMCP,
		DecideApprovalResponseEnvelopeBuilder{Projection: projection},
		newDecideApprovalWriterKernel(writer),
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
