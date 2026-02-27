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
	transportSurface, ok := transportWriterSurfaceForDecideApproval(surface)
	if !ok {
		dispatchDecideApprovalUnsupportedEnvelope(unsupportedDecideApprovalResponseEnvelope(string(surface)), writer)
		return
	}

	transportwriter.WriteFromBuilder(transportSurface, builder, transportwriter.WriterKernel{
		WriteHTTPError: func(err *transportwriter.HTTPError) {
			if writer.WriteHTTPError != nil {
				writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
			}
		},
		WriteMCPError: writer.WriteMCPError,
		WriteHTTPSuccess: func(payload any) {
			if writer.WriteSuccess == nil {
				return
			}
			success, _ := payload.(*DecideApprovalSuccess)
			writer.WriteSuccess(normalizeDecideApprovalSuccess(success))
		},
		WriteMCPSuccess: func(payload any) {
			if writer.WriteSuccess == nil {
				return
			}
			success, _ := payload.(*DecideApprovalSuccess)
			writer.WriteSuccess(normalizeDecideApprovalSuccess(success))
		},
	})
}

func dispatchDecideApprovalUnsupportedEnvelope(envelope *transportwriter.ResponseEnvelope, writer DecideApprovalResponseDispatchWriter) {
	if writer.WriteHTTPError != nil && envelope != nil && envelope.HTTPError != nil {
		err := envelope.HTTPError
		writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
		return
	}
	if writer.WriteMCPError != nil && envelope != nil && envelope.MCPError != nil {
		writer.WriteMCPError(envelope.MCPError)
	}
}

func transportWriterSurfaceForDecideApproval(surface DecideApprovalRenderSurface) (transportwriter.Surface, bool) {
	resolvedTarget, ok := ResolveDecideApprovalRenderTarget(surface)
	if !ok {
		return "", false
	}
	return transportwriter.Surface(resolvedTarget), true
}
