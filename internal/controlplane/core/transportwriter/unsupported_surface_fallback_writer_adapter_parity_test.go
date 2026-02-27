package transportwriter

import (
	"errors"
	"reflect"
	"testing"
)

type testUnsupportedSurfaceHTTPContract struct {
	Status  int
	Code    string
	Message string
}

func newTestUnsupportedSurfaceHTTPContract(status int, code, message string) *testUnsupportedSurfaceHTTPContract {
	return &testUnsupportedSurfaceHTTPContract{Status: status, Code: code, Message: message}
}

func TestAdaptUnsupportedSurfaceFallbackWriter_ParityWithLegacyDomainConstructor(t *testing.T) {
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

	envelope := &ResponseEnvelope{
		HTTPError: &HTTPError{Status: 500, Code: "internal_error", Message: "unsupported surface"},
		MCPError:  errors.New("unsupported surface"),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := unsupportedSurfaceFallbackCapture{}
			legacyCapture := unsupportedSurfaceFallbackCapture{}

			newWriteHTTP, newWriteMCP := newCapture.writers(tt.withHTTPError, tt.withMCPError)
			legacyWriteHTTP, legacyWriteMCP := legacyCapture.writers(tt.withHTTPError, tt.withMCPError)

			newHandled := WriteUnsupportedSurfaceFallback(envelope, AdaptUnsupportedSurfaceFallbackWriter(newWriteHTTP, newTestUnsupportedSurfaceHTTPContract, newWriteMCP))
			legacyHandled := WriteUnsupportedSurfaceFallback(envelope, legacyUnsupportedSurfaceFallbackWriter(legacyWriteHTTP, newTestUnsupportedSurfaceHTTPContract, legacyWriteMCP))

			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
			if !newCapture.equal(legacyCapture) {
				t.Fatalf("capture parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

type unsupportedSurfaceFallbackCapture struct {
	httpErrCalled bool
	httpErr       *testUnsupportedSurfaceHTTPContract
	mcpErrCalled  bool
	mcpErr        error
}

func (c *unsupportedSurfaceFallbackCapture) writers(withHTTPError, withMCPError bool) (func(*testUnsupportedSurfaceHTTPContract), func(error)) {
	var writeHTTPError func(*testUnsupportedSurfaceHTTPContract)
	if withHTTPError {
		writeHTTPError = func(err *testUnsupportedSurfaceHTTPContract) {
			c.httpErrCalled = true
			c.httpErr = err
		}
	}

	var writeMCPError func(error)
	if withMCPError {
		writeMCPError = func(err error) {
			c.mcpErrCalled = true
			c.mcpErr = err
		}
	}

	return writeHTTPError, writeMCPError
}

func (c unsupportedSurfaceFallbackCapture) equal(other unsupportedSurfaceFallbackCapture) bool {
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

func legacyUnsupportedSurfaceFallbackWriter[T any](writeHTTPError func(*T), constructHTTPError func(status int, code, message string) *T, writeMCPError func(error)) UnsupportedSurfaceFallbackWriter {
	fallbackWriter := UnsupportedSurfaceFallbackWriter{WriteMCPError: writeMCPError}
	if writeHTTPError != nil {
		fallbackWriter.WriteHTTPError = func(err *HTTPError) {
			if err == nil {
				return
			}
			writeHTTPError(constructHTTPError(err.Status, err.Code, err.Message))
		}
	}
	return fallbackWriter
}
