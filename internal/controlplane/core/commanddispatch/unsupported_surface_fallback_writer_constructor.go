package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

// newCommandUnsupportedSurfaceFallbackWriter assembles unsupported-surface
// fallback callbacks from command-domain dispatch writers.
func newCommandUnsupportedSurfaceFallbackWriter(writer CommandProjectionDispatchWriter) transportwriter.UnsupportedSurfaceFallbackWriter {
	return transportwriter.UnsupportedSurfaceFallbackWriter{
		WriteHTTPError: adaptCommandHTTPErrorWriter(writer.WriteHTTPError),
		WriteMCPError:  writer.WriteMCPError,
	}
}
