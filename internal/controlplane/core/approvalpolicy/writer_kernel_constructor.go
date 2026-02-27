package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

// newDecideApprovalWriterKernel assembles the shared transport-writer kernel
// from approval-domain callbacks.
func newDecideApprovalWriterKernel(writer DecideApprovalResponseDispatchWriter) transportwriter.WriterKernel {
	successWriter := adaptApprovalSuccessPayloadWriter(writer.WriteSuccess)

	return transportwriter.WriterKernel{
		WriteHTTPError:   adaptApprovalHTTPErrorWriter(writer.WriteHTTPError),
		WriteMCPError:    writer.WriteMCPError,
		WriteHTTPSuccess: successWriter,
		WriteMCPSuccess:  successWriter,
	}
}
