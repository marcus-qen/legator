package transportwriter

import (
	"reflect"
	"testing"
)

func TestDispatchUnsupportedSurfaceFallback_ParityWithLegacyRepeatedCallShape(t *testing.T) {
	tests := []struct {
		name     string
		withHTTP bool
		withMCP  bool
	}{
		{name: "http first when both callbacks present", withHTTP: true, withMCP: true},
		{name: "http only", withHTTP: true, withMCP: false},
		{name: "mcp fallback when http callback absent", withHTTP: false, withMCP: true},
		{name: "no callbacks", withHTTP: false, withMCP: false},
	}

	const surface = "bogus"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := unsupportedSurfaceDispatchCapture{}
			legacyCapture := unsupportedSurfaceDispatchCapture{}

			newEnvelopeCalls, newWriterCalls := 0, 0
			legacyEnvelopeCalls, legacyWriterCalls := 0, 0

			newWriter := newCapture.writer(tt.withHTTP, tt.withMCP)
			legacyWriter := legacyCapture.writer(tt.withHTTP, tt.withMCP)

			newHandled := DispatchUnsupportedSurfaceFallback(
				surface,
				func(surface string) *ResponseEnvelope {
					newEnvelopeCalls++
					return unsupportedSurfaceDispatchEnvelope(surface)
				},
				newWriter,
				func(writer unsupportedSurfaceDispatchDomainWriter) UnsupportedSurfaceFallbackWriter {
					newWriterCalls++
					return newUnsupportedSurfaceDispatchFallbackWriter(writer)
				},
			)

			legacyHandled := WriteUnsupportedSurfaceFallback(
				func() *ResponseEnvelope {
					legacyEnvelopeCalls++
					return unsupportedSurfaceDispatchEnvelope(surface)
				}(),
				func() UnsupportedSurfaceFallbackWriter {
					legacyWriterCalls++
					return newUnsupportedSurfaceDispatchFallbackWriter(legacyWriter)
				}(),
			)

			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if newEnvelopeCalls != legacyEnvelopeCalls {
				t.Fatalf("envelope build call parity mismatch: new=%d legacy=%d", newEnvelopeCalls, legacyEnvelopeCalls)
			}
			if newWriterCalls != legacyWriterCalls {
				t.Fatalf("writer build call parity mismatch: new=%d legacy=%d", newWriterCalls, legacyWriterCalls)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("capture parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

type unsupportedSurfaceDispatchDomainWriter struct {
	WriteHTTPError func(*testUnsupportedSurfaceHTTPContract)
	WriteMCPError  func(error)
}

type unsupportedSurfaceDispatchCapture struct {
	httpErrCalled bool
	httpErr       *testUnsupportedSurfaceHTTPContract
	mcpErrCalled  bool
	mcpErr        error
}

func (c *unsupportedSurfaceDispatchCapture) writer(withHTTP, withMCP bool) unsupportedSurfaceDispatchDomainWriter {
	writer := unsupportedSurfaceDispatchDomainWriter{}
	if withHTTP {
		writer.WriteHTTPError = func(err *testUnsupportedSurfaceHTTPContract) {
			c.httpErrCalled = true
			c.httpErr = err
		}
	}
	if withMCP {
		writer.WriteMCPError = func(err error) {
			c.mcpErrCalled = true
			c.mcpErr = err
		}
	}
	return writer
}

func (c unsupportedSurfaceDispatchCapture) equal(other unsupportedSurfaceDispatchCapture) bool {
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

func unsupportedSurfaceDispatchEnvelope(surface string) *ResponseEnvelope {
	return UnsupportedSurfaceEnvelope(UnsupportedSurfaceMessage(UnsupportedSurfaceScopeCommandDispatch, surface))
}

func newUnsupportedSurfaceDispatchFallbackWriter(writer unsupportedSurfaceDispatchDomainWriter) UnsupportedSurfaceFallbackWriter {
	return AdaptUnsupportedSurfaceFallbackWriter(writer.WriteHTTPError, newTestUnsupportedSurfaceHTTPContract, writer.WriteMCPError)
}
