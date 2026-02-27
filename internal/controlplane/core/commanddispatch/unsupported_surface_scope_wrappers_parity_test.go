package commanddispatch

import (
	"net/http"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestUnsupportedCommandSurfaceMessages_ParityWithTransportScopes(t *testing.T) {
	const surface = "bogus"

	if got, want := unsupportedCommandInvokeSurfaceMessage(surface), transportwriter.UnsupportedSurfaceMessage(transportwriter.UnsupportedSurfaceScopeCommandInvoke, surface); got != want {
		t.Fatalf("command invoke unsupported-surface message parity mismatch: got %q want %q", got, want)
	}

	dispatchSurface := ProjectionDispatchSurface(surface)
	if got, want := unsupportedCommandDispatchSurfaceMessage(dispatchSurface), transportwriter.UnsupportedSurfaceMessage(transportwriter.UnsupportedSurfaceScopeCommandDispatch, surface); got != want {
		t.Fatalf("command dispatch unsupported-surface message parity mismatch: got %q want %q", got, want)
	}
}

func TestUnsupportedCommandInvokeResponseEnvelope_Parity(t *testing.T) {
	const wantMessage = "unsupported command invoke surface \"bogus\""
	envelope := unsupportedCommandInvokeResponseEnvelope("bogus")
	if envelope == nil || envelope.HTTPError == nil || envelope.MCPError == nil {
		t.Fatalf("expected unsupported-surface envelope with HTTP+MCP errors, got %+v", envelope)
	}
	if envelope.HTTPError.Status != http.StatusInternalServerError || envelope.HTTPError.Code != "internal_error" || envelope.HTTPError.Message != wantMessage {
		t.Fatalf("unexpected unsupported-surface HTTP envelope error: %+v", envelope.HTTPError)
	}
	if envelope.MCPError.Error() != wantMessage {
		t.Fatalf("unexpected unsupported-surface MCP envelope error: %v", envelope.MCPError)
	}
}

func TestUnsupportedCommandDispatchResponseEnvelope_FallbackParity(t *testing.T) {
	const wantMessage = "unsupported command dispatch surface \"bogus\""
	envelope := unsupportedCommandDispatchResponseEnvelope(ProjectionDispatchSurface("bogus"))

	httpCalled, mcpCalled := false, false
	transportwriter.WriteUnsupportedSurfaceFallback(envelope, transportwriter.UnsupportedSurfaceFallbackWriter{
		WriteHTTPError: func(err *transportwriter.HTTPError) {
			httpCalled = true
			if err == nil || err.Status != http.StatusInternalServerError || err.Code != "internal_error" || err.Message != wantMessage {
				t.Fatalf("unexpected unsupported-surface HTTP fallback: %+v", err)
			}
		},
		WriteMCPError: func(err error) {
			mcpCalled = err != nil
		},
	})
	if !httpCalled || mcpCalled {
		t.Fatalf("fallback precedence mismatch: http=%v mcp=%v", httpCalled, mcpCalled)
	}

	var got error
	transportwriter.WriteUnsupportedSurfaceFallback(envelope, transportwriter.UnsupportedSurfaceFallbackWriter{
		WriteMCPError: func(err error) {
			got = err
		},
	})
	if got == nil || got.Error() != wantMessage {
		t.Fatalf("unexpected unsupported-surface MCP fallback: %v", got)
	}
}
