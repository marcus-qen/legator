package approvalpolicy

import (
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
	transportSurface, ok := ResolveDecideApprovalTransportSurface(surface)
	if !ok {
		dispatchUnsupportedDecideApprovalSurfaceFallback(string(surface), writer)
		return
	}

	successWriter := adaptApprovalSuccessPayloadWriter(writer.WriteSuccess)

	transportwriter.WriteFromBuilder(transportSurface, builder, transportwriter.WriterKernel{
		WriteHTTPError:   adaptApprovalHTTPErrorWriter(writer.WriteHTTPError),
		WriteMCPError:    writer.WriteMCPError,
		WriteHTTPSuccess: successWriter,
		WriteMCPSuccess:  successWriter,
	})
}
