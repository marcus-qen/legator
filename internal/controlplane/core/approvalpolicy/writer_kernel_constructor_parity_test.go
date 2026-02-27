package approvalpolicy

import (
	"errors"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestNewDecideApprovalWriterKernel_ParityWithLegacyInlineWiring(t *testing.T) {
	var typedNilSuccess *DecideApprovalSuccess

	tests := []struct {
		name          string
		surface       transportwriter.Surface
		envelope      *transportwriter.ResponseEnvelope
		withHTTPError bool
		withMCPError  bool
		withSuccess   bool
	}{
		{
			name:    "http error mapping parity",
			surface: transportwriter.SurfaceHTTP,
			envelope: &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
				Status:  502,
				Code:    "bad_gateway",
				Message: "probe down",
			}},
			withHTTPError: true,
		},
		{
			name:    "http suppressed error parity",
			surface: transportwriter.SurfaceHTTP,
			envelope: &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
				Status:        500,
				Code:          "internal_error",
				Message:       "suppressed",
				SuppressWrite: true,
			}},
			withHTTPError: true,
		},
		{
			name:          "http success typed payload parity",
			surface:       transportwriter.SurfaceHTTP,
			envelope:      &transportwriter.ResponseEnvelope{HTTPSuccess: &DecideApprovalSuccess{Status: "approved"}},
			withSuccess:   true,
			withHTTPError: true,
		},
		{
			name:        "http success typed nil payload parity",
			surface:     transportwriter.SurfaceHTTP,
			envelope:    &transportwriter.ResponseEnvelope{HTTPSuccess: typedNilSuccess},
			withSuccess: true,
		},
		{
			name:        "http success wrong payload parity",
			surface:     transportwriter.SurfaceHTTP,
			envelope:    &transportwriter.ResponseEnvelope{HTTPSuccess: "wrong"},
			withSuccess: true,
		},
		{
			name:         "mcp error parity",
			surface:      transportwriter.SurfaceMCP,
			envelope:     &transportwriter.ResponseEnvelope{MCPError: errors.New("dispatch failed")},
			withMCPError: true,
		},
		{
			name:        "mcp success typed payload parity",
			surface:     transportwriter.SurfaceMCP,
			envelope:    &transportwriter.ResponseEnvelope{MCPSuccess: &DecideApprovalSuccess{Status: "denied"}},
			withSuccess: true,
		},
		{
			name:        "mcp success nil payload parity",
			surface:     transportwriter.SurfaceMCP,
			envelope:    &transportwriter.ResponseEnvelope{MCPSuccess: nil},
			withSuccess: true,
		},
		{
			name:    "nil envelope parity",
			surface: transportwriter.SurfaceHTTP,
		},
		{
			name:    "callbacks nil parity",
			surface: transportwriter.SurfaceHTTP,
			envelope: &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
				Status:  500,
				Code:    "internal_error",
				Message: "boom",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := approvalKernelCapture{}
			legacyCapture := approvalKernelCapture{}

			newWriter := newCapture.writer(tt.withHTTPError, tt.withMCPError, tt.withSuccess)
			legacyWriter := legacyCapture.writer(tt.withHTTPError, tt.withMCPError, tt.withSuccess)

			newHandled := transportwriter.WriteForSurface(tt.surface, tt.envelope, newDecideApprovalWriterKernel(newWriter))
			legacyHandled := transportwriter.WriteForSurface(tt.surface, tt.envelope, legacyDecideApprovalWriterKernel(legacyWriter))

			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("capture parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

type approvalKernelCapture struct {
	httpErr *HTTPErrorContract
	mcpErr  error
	success *DecideApprovalSuccess
}

func (c *approvalKernelCapture) writer(withHTTPError, withMCPError, withSuccess bool) DecideApprovalResponseDispatchWriter {
	writer := DecideApprovalResponseDispatchWriter{}
	if withHTTPError {
		writer.WriteHTTPError = func(err *HTTPErrorContract) {
			c.httpErr = err
		}
	}
	if withMCPError {
		writer.WriteMCPError = func(err error) {
			c.mcpErr = err
		}
	}
	if withSuccess {
		writer.WriteSuccess = func(success *DecideApprovalSuccess) {
			c.success = success
		}
	}
	return writer
}

func (c approvalKernelCapture) equal(other approvalKernelCapture) bool {
	if !reflect.DeepEqual(c.httpErr, other.httpErr) {
		return false
	}
	if !reflect.DeepEqual(c.success, other.success) {
		return false
	}
	switch {
	case c.mcpErr == nil && other.mcpErr == nil:
		return true
	case c.mcpErr == nil || other.mcpErr == nil:
		return false
	default:
		return c.mcpErr.Error() == other.mcpErr.Error()
	}
}

func legacyDecideApprovalWriterKernel(writer DecideApprovalResponseDispatchWriter) transportwriter.WriterKernel {
	legacySuccessWriter := legacyApprovalSuccessPayloadWriter(writer.WriteSuccess)

	return transportwriter.WriterKernel{
		WriteHTTPError:   legacyApprovalHTTPErrorWriter(writer.WriteHTTPError),
		WriteMCPError:    writer.WriteMCPError,
		WriteHTTPSuccess: legacySuccessWriter,
		WriteMCPSuccess:  legacySuccessWriter,
	}
}
