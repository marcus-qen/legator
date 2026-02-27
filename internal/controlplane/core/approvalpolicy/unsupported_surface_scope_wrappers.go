package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

const unsupportedDecideApprovalScope = transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch

func unsupportedDecideApprovalSurfaceMessage(surface string) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedDecideApprovalScope, surface)
}

func unsupportedDecideApprovalSurfaceEnvelope(surface string) *transportwriter.ResponseEnvelope {
	return transportwriter.UnsupportedSurfaceEnvelope(unsupportedDecideApprovalSurfaceMessage(surface))
}

func dispatchUnsupportedDecideApprovalSurfaceFallback(surface string, writer DecideApprovalResponseDispatchWriter) {
	fallbackWriter := transportwriter.UnsupportedSurfaceFallbackWriter{WriteMCPError: writer.WriteMCPError}
	if writer.WriteHTTPError != nil {
		fallbackWriter.WriteHTTPError = func(err *transportwriter.HTTPError) {
			if err == nil {
				return
			}
			writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
		}
	}
	transportwriter.WriteUnsupportedSurfaceFallback(unsupportedDecideApprovalSurfaceEnvelope(surface), fallbackWriter)
}
