package commanddispatch

import (
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestAdaptCommandHTTPErrorWriter_ParityWithLegacyInlineConversion(t *testing.T) {
	tests := []struct {
		name string
		err  *transportwriter.HTTPError
	}{
		{name: "nil error", err: nil},
		{name: "status/code/message parity", err: &transportwriter.HTTPError{Status: 500, Code: "internal_error", Message: "boom", SuppressWrite: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotNew *HTTPErrorContract
			adaptedNew := adaptCommandHTTPErrorWriter(func(contract *HTTPErrorContract) {
				gotNew = contract
			})

			var gotLegacy *HTTPErrorContract
			adaptedLegacy := legacyCommandHTTPErrorWriter(func(contract *HTTPErrorContract) {
				gotLegacy = contract
			})

			adaptedNew(tt.err)
			adaptedLegacy(tt.err)

			if !reflect.DeepEqual(gotNew, gotLegacy) {
				t.Fatalf("conversion parity mismatch: new=%+v legacy=%+v", gotNew, gotLegacy)
			}
		})
	}

	if got := adaptCommandHTTPErrorWriter(nil); got != nil {
		t.Fatalf("expected nil writer adapter for nil callback, got type %T", got)
	}
}

func TestDispatchUnsupportedCommandDispatchSurfaceFallback_EndToEndParity(t *testing.T) {
	surface := ProjectionDispatchSurface("bogus")

	tests := []struct {
		name     string
		withHTTP bool
		withMCP  bool
	}{
		{name: "http+mcp callbacks", withHTTP: true, withMCP: true},
		{name: "mcp only", withHTTP: false, withMCP: true},
		{name: "http only", withHTTP: true, withMCP: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := commandFallbackCapture{}
			dispatchUnsupportedCommandDispatchSurfaceFallback(surface, newCapture.writer(tt.withHTTP, tt.withMCP))

			legacyCapture := commandFallbackCapture{}
			legacyDispatchUnsupportedCommandDispatchSurfaceFallback(surface, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if !reflect.DeepEqual(newCapture.httpErr, legacyCapture.httpErr) {
				t.Fatalf("http fallback parity mismatch: new=%+v legacy=%+v", newCapture.httpErr, legacyCapture.httpErr)
			}
			if (newCapture.mcpErr == nil) != (legacyCapture.mcpErr == nil) {
				t.Fatalf("mcp fallback nil parity mismatch: new=%v legacy=%v", newCapture.mcpErr, legacyCapture.mcpErr)
			}
			if newCapture.mcpErr != nil && newCapture.mcpErr.Error() != legacyCapture.mcpErr.Error() {
				t.Fatalf("mcp fallback parity mismatch: new=%q legacy=%q", newCapture.mcpErr.Error(), legacyCapture.mcpErr.Error())
			}
		})
	}
}

type commandFallbackCapture struct {
	httpErr *HTTPErrorContract
	mcpErr  error
}

func (c *commandFallbackCapture) writer(withHTTP, withMCP bool) CommandProjectionDispatchWriter {
	writer := CommandProjectionDispatchWriter{}
	if withHTTP {
		writer.WriteHTTPError = func(err *HTTPErrorContract) {
			c.httpErr = err
		}
	}
	if withMCP {
		writer.WriteMCPError = func(err error) {
			c.mcpErr = err
		}
	}
	return writer
}

func legacyCommandHTTPErrorWriter(write func(*HTTPErrorContract)) func(*transportwriter.HTTPError) {
	if write == nil {
		return nil
	}
	return func(err *transportwriter.HTTPError) {
		if err == nil {
			return
		}
		write(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
	}
}

func legacyDispatchUnsupportedCommandDispatchSurfaceFallback(surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
	fallbackWriter := transportwriter.UnsupportedSurfaceFallbackWriter{WriteMCPError: writer.WriteMCPError}
	if writer.WriteHTTPError != nil {
		fallbackWriter.WriteHTTPError = func(err *transportwriter.HTTPError) {
			if err == nil {
				return
			}
			writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
		}
	}
	transportwriter.WriteUnsupportedSurfaceFallback(unsupportedCommandDispatchSurfaceEnvelope(surface), fallbackWriter)
}
