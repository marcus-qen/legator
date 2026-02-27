package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

// CommandInvokeWriterKernelCallbacks defines command-domain callbacks used to
// assemble a shared transport writer kernel.
type CommandInvokeWriterKernelCallbacks struct {
	WriteHTTPError   func(*HTTPErrorContract)
	WriteMCPError    func(error)
	WriteHTTPSuccess func(any)
	WriteMCPSuccess  func(string)
}

// newCommandInvokeWriterKernel assembles the shared transport-writer kernel
// from command-domain callbacks.
func newCommandInvokeWriterKernel(callbacks CommandInvokeWriterKernelCallbacks) transportwriter.WriterKernel {
	return transportwriter.WriterKernel{
		WriteHTTPError:   adaptCommandHTTPErrorWriter(callbacks.WriteHTTPError),
		WriteMCPError:    callbacks.WriteMCPError,
		WriteHTTPSuccess: callbacks.WriteHTTPSuccess,
		WriteMCPSuccess:  adaptCommandMCPSuccessPayloadWriter(callbacks.WriteMCPSuccess),
	}
}
