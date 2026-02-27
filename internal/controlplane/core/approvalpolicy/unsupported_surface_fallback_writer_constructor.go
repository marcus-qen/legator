package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

// newDecideApprovalUnsupportedSurfaceFallbackWriter assembles unsupported-
// surface fallback callbacks from approval-domain dispatch writers.
func newDecideApprovalUnsupportedSurfaceFallbackWriter(writer DecideApprovalResponseDispatchWriter) transportwriter.UnsupportedSurfaceFallbackWriter {
	return transportwriter.AdaptUnsupportedSurfaceFallbackWriter(
		writer.WriteHTTPError,
		newApprovalHTTPErrorContract,
		writer.WriteMCPError,
	)
}
