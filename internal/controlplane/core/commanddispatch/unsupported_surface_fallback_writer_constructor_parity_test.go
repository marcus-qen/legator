package commanddispatch

import (
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestNewCommandUnsupportedSurfaceFallbackWriter_ParityWithLegacyInlineWiring(t *testing.T) {
	tests := []struct {
		name          string
		withHTTPError bool
		withMCPError  bool
	}{
		{name: "http first when both callbacks present", withHTTPError: true, withMCPError: true},
		{name: "http only", withHTTPError: true, withMCPError: false},
		{name: "mcp fallback when http callback absent", withHTTPError: false, withMCPError: true},
		{name: "no callbacks", withHTTPError: false, withMCPError: false},
	}

	envelope := unsupportedCommandDispatchSurfaceEnvelope(ProjectionDispatchSurface("bogus"))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandUnsupportedFallbackCapture{}
			legacyCapture := commandUnsupportedFallbackCapture{}

			newWriter := newCapture.writer(tt.withHTTPError, tt.withMCPError)
			legacyWriter := legacyCapture.writer(tt.withHTTPError, tt.withMCPError)

			newHandled := transportwriter.WriteUnsupportedSurfaceFallback(envelope, newCommandUnsupportedSurfaceFallbackWriter(newWriter))
			legacyHandled := transportwriter.WriteUnsupportedSurfaceFallback(envelope, legacyCommandUnsupportedSurfaceFallbackWriter(legacyWriter))

			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("capture parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

type commandUnsupportedFallbackCapture struct {
	httpErrCalled bool
	httpErr       *HTTPErrorContract
	mcpErrCalled  bool
	mcpErr        error
}

func (c *commandUnsupportedFallbackCapture) writer(withHTTPError, withMCPError bool) CommandProjectionDispatchWriter {
	writer := CommandProjectionDispatchWriter{}
	if withHTTPError {
		writer.WriteHTTPError = func(err *HTTPErrorContract) {
			c.httpErrCalled = true
			c.httpErr = err
		}
	}
	if withMCPError {
		writer.WriteMCPError = func(err error) {
			c.mcpErrCalled = true
			c.mcpErr = err
		}
	}
	return writer
}

func (c commandUnsupportedFallbackCapture) equal(other commandUnsupportedFallbackCapture) bool {
	if c.httpErrCalled != other.httpErrCalled || !reflect.DeepEqual(c.httpErr, other.httpErr) {
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
	return true
}

func legacyCommandUnsupportedSurfaceFallbackWriter(writer CommandProjectionDispatchWriter) transportwriter.UnsupportedSurfaceFallbackWriter {
	fallbackWriter := transportwriter.UnsupportedSurfaceFallbackWriter{WriteMCPError: writer.WriteMCPError}
	if writer.WriteHTTPError != nil {
		fallbackWriter.WriteHTTPError = func(err *transportwriter.HTTPError) {
			if err == nil {
				return
			}
			writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
		}
	}
	return fallbackWriter
}
