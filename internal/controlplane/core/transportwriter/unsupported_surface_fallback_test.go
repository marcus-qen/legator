package transportwriter

import (
	"errors"
	"net/http"
	"testing"
)

func TestWriteUnsupportedSurfaceFallback_HTTPPrecedesMCP(t *testing.T) {
	envelope := &ResponseEnvelope{
		HTTPError: &HTTPError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "unsupported surface"},
		MCPError:  errors.New("unsupported surface"),
	}

	httpCalled, mcpCalled := false, false
	handled := WriteUnsupportedSurfaceFallback(envelope, UnsupportedSurfaceFallbackWriter{
		WriteHTTPError: func(err *HTTPError) {
			httpCalled = true
			if err == nil || err.Status != http.StatusInternalServerError || err.Code != "internal_error" || err.Message != "unsupported surface" {
				t.Fatalf("unexpected HTTP fallback error: %+v", err)
			}
		},
		WriteMCPError: func(err error) {
			mcpCalled = err != nil
		},
	})

	if !handled {
		t.Fatal("expected handled=true")
	}
	if !httpCalled || mcpCalled {
		t.Fatalf("unexpected fallback callbacks: http=%v mcp=%v", httpCalled, mcpCalled)
	}
}

func TestWriteUnsupportedSurfaceFallback_FallsBackToMCPWhenHTTPMissing(t *testing.T) {
	envelope := &ResponseEnvelope{
		HTTPError: &HTTPError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "unsupported surface"},
		MCPError:  errors.New("unsupported surface"),
	}

	var got error
	handled := WriteUnsupportedSurfaceFallback(envelope, UnsupportedSurfaceFallbackWriter{
		WriteMCPError: func(err error) {
			got = err
		},
	})

	if !handled {
		t.Fatal("expected handled=true")
	}
	if got == nil || got.Error() != "unsupported surface" {
		t.Fatalf("unexpected MCP fallback error: %v", got)
	}
}

func TestWriteUnsupportedSurfaceFallback_UnhandledWhenNoEligibleWriter(t *testing.T) {
	envelope := &ResponseEnvelope{
		HTTPError: &HTTPError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "unsupported surface"},
		MCPError:  errors.New("unsupported surface"),
	}

	if handled := WriteUnsupportedSurfaceFallback(envelope, UnsupportedSurfaceFallbackWriter{}); handled {
		t.Fatal("expected handled=false when no callbacks are provided")
	}
}
