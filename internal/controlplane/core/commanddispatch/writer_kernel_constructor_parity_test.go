package commanddispatch

import (
	"errors"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestNewCommandInvokeWriterKernel_ParityWithLegacyInlineWiring(t *testing.T) {
	tests := []struct {
		name            string
		surface         transportwriter.Surface
		envelope        *transportwriter.ResponseEnvelope
		withHTTPError   bool
		withMCPError    bool
		withHTTPSuccess bool
		withMCPSuccess  bool
	}{
		{
			name:    "http error mapping parity",
			surface: transportwriter.SurfaceHTTP,
			envelope: &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
				Status:  500,
				Code:    "internal_error",
				Message: "boom",
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
			name:            "http success payload parity",
			surface:         transportwriter.SurfaceHTTP,
			envelope:        &transportwriter.ResponseEnvelope{HTTPSuccess: map[string]string{"status": "dispatched"}},
			withHTTPSuccess: true,
		},
		{
			name:           "mcp error parity",
			surface:        transportwriter.SurfaceMCP,
			envelope:       &transportwriter.ResponseEnvelope{MCPError: errors.New("dispatch failed")},
			withMCPError:   true,
			withMCPSuccess: true,
		},
		{
			name:           "mcp success payload parity",
			surface:        transportwriter.SurfaceMCP,
			envelope:       &transportwriter.ResponseEnvelope{MCPSuccess: "ok"},
			withMCPSuccess: true,
		},
		{
			name:           "mcp success nil payload parity",
			surface:        transportwriter.SurfaceMCP,
			envelope:       &transportwriter.ResponseEnvelope{MCPSuccess: nil},
			withMCPSuccess: true,
		},
		{
			name:           "mcp success wrong payload parity",
			surface:        transportwriter.SurfaceMCP,
			envelope:       &transportwriter.ResponseEnvelope{MCPSuccess: 42},
			withMCPSuccess: true,
		},
		{
			name:    "nil envelope parity",
			surface: transportwriter.SurfaceHTTP,
		},
		{
			name:            "callbacks nil parity",
			surface:         transportwriter.SurfaceHTTP,
			envelope:        &transportwriter.ResponseEnvelope{HTTPSuccess: "ok"},
			withHTTPSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandKernelCapture{}
			legacyCapture := commandKernelCapture{}

			newCallbacks := newCapture.callbacks(tt.withHTTPError, tt.withMCPError, tt.withHTTPSuccess, tt.withMCPSuccess)
			legacyCallbacks := legacyCapture.callbacks(tt.withHTTPError, tt.withMCPError, tt.withHTTPSuccess, tt.withMCPSuccess)

			newHandled := transportwriter.WriteForSurface(tt.surface, tt.envelope, newCommandInvokeWriterKernel(newCallbacks))
			legacyHandled := transportwriter.WriteForSurface(tt.surface, tt.envelope, legacyCommandInvokeWriterKernel(legacyCallbacks))

			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("capture parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

type commandKernelCapture struct {
	httpErrCalled     bool
	httpErr           *HTTPErrorContract
	httpSuccessCalled bool
	httpSuccess       any
	mcpErrCalled      bool
	mcpErr            error
	mcpSuccessCalled  bool
	mcpSuccess        string
}

func (c *commandKernelCapture) callbacks(withHTTPError, withMCPError, withHTTPSuccess, withMCPSuccess bool) CommandInvokeWriterKernelCallbacks {
	callbacks := CommandInvokeWriterKernelCallbacks{}
	if withHTTPError {
		callbacks.WriteHTTPError = func(err *HTTPErrorContract) {
			c.httpErrCalled = true
			c.httpErr = err
		}
	}
	if withMCPError {
		callbacks.WriteMCPError = func(err error) {
			c.mcpErrCalled = true
			c.mcpErr = err
		}
	}
	if withHTTPSuccess {
		callbacks.WriteHTTPSuccess = func(payload any) {
			c.httpSuccessCalled = true
			c.httpSuccess = payload
		}
	}
	if withMCPSuccess {
		callbacks.WriteMCPSuccess = func(text string) {
			c.mcpSuccessCalled = true
			c.mcpSuccess = text
		}
	}
	return callbacks
}

func (c commandKernelCapture) equal(other commandKernelCapture) bool {
	if c.httpErrCalled != other.httpErrCalled || !reflect.DeepEqual(c.httpErr, other.httpErr) {
		return false
	}
	if c.httpSuccessCalled != other.httpSuccessCalled || !reflect.DeepEqual(c.httpSuccess, other.httpSuccess) {
		return false
	}
	if c.mcpErrCalled != other.mcpErrCalled {
		return false
	}
	switch {
	case c.mcpErr == nil && other.mcpErr == nil:
	case c.mcpErr == nil || other.mcpErr == nil:
		return false
	default:
		if c.mcpErr.Error() != other.mcpErr.Error() {
			return false
		}
	}
	if c.mcpSuccessCalled != other.mcpSuccessCalled || c.mcpSuccess != other.mcpSuccess {
		return false
	}
	return true
}

func legacyCommandInvokeWriterKernel(callbacks CommandInvokeWriterKernelCallbacks) transportwriter.WriterKernel {
	return transportwriter.WriterKernel{
		WriteHTTPError:   legacyCommandHTTPErrorWriter(callbacks.WriteHTTPError),
		WriteMCPError:    callbacks.WriteMCPError,
		WriteHTTPSuccess: callbacks.WriteHTTPSuccess,
		WriteMCPSuccess:  legacyCommandMCPSuccessPayloadWriter(callbacks.WriteMCPSuccess),
	}
}
