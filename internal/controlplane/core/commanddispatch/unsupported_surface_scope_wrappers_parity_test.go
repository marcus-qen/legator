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

func TestUnsupportedCommandSurfaceEnvelopes_ParityWithTransportFactory(t *testing.T) {
	const surface = "bogus"
	dispatchSurface := ProjectionDispatchSurface(surface)

	invokeMessage := transportwriter.UnsupportedSurfaceMessage(transportwriter.UnsupportedSurfaceScopeCommandInvoke, surface)
	assertUnsupportedSurfaceEnvelopeParity(t, unsupportedCommandInvokeSurfaceEnvelope(surface), transportwriter.UnsupportedSurfaceEnvelope(invokeMessage), invokeMessage)

	dispatchMessage := transportwriter.UnsupportedSurfaceMessage(transportwriter.UnsupportedSurfaceScopeCommandDispatch, surface)
	assertUnsupportedSurfaceEnvelopeParity(t, unsupportedCommandDispatchSurfaceEnvelope(dispatchSurface), transportwriter.UnsupportedSurfaceEnvelope(dispatchMessage), dispatchMessage)
}

func TestUnsupportedCommandInvokeResponseEnvelope_Parity(t *testing.T) {
	const surface = "bogus"
	const wantMessage = "unsupported command invoke surface \"bogus\""
	envelope := unsupportedCommandInvokeResponseEnvelope(surface)
	assertUnsupportedSurfaceEnvelopeParity(t, envelope, unsupportedCommandInvokeSurfaceEnvelope(surface), wantMessage)
}

func TestUnsupportedCommandDispatchResponseEnvelope_FallbackParity(t *testing.T) {
	const wantMessage = "unsupported command dispatch surface \"bogus\""
	envelope := unsupportedCommandDispatchResponseEnvelope(ProjectionDispatchSurface("bogus"))
	assertUnsupportedSurfaceEnvelopeParity(t, envelope, unsupportedCommandDispatchSurfaceEnvelope(ProjectionDispatchSurface("bogus")), wantMessage)

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

func assertUnsupportedSurfaceEnvelopeParity(t *testing.T, got, want *transportwriter.ResponseEnvelope, wantMessage string) {
	t.Helper()
	if got == nil || want == nil {
		t.Fatalf("expected non-nil envelopes, got=%+v want=%+v", got, want)
	}
	if got.HTTPError == nil || want.HTTPError == nil {
		t.Fatalf("expected unsupported-surface HTTP errors, got=%+v want=%+v", got.HTTPError, want.HTTPError)
	}
	if got.HTTPError.Status != want.HTTPError.Status || got.HTTPError.Code != want.HTTPError.Code || got.HTTPError.Message != want.HTTPError.Message {
		t.Fatalf("unsupported-surface HTTP envelope mismatch: got=%+v want=%+v", got.HTTPError, want.HTTPError)
	}
	if got.HTTPError.Status != http.StatusInternalServerError || got.HTTPError.Code != "internal_error" || got.HTTPError.Message != wantMessage {
		t.Fatalf("unexpected unsupported-surface HTTP envelope semantics: %+v", got.HTTPError)
	}
	if got.MCPError == nil || want.MCPError == nil {
		t.Fatalf("expected unsupported-surface MCP errors, got=%v want=%v", got.MCPError, want.MCPError)
	}
	if got.MCPError.Error() != want.MCPError.Error() {
		t.Fatalf("unsupported-surface MCP envelope mismatch: got=%q want=%q", got.MCPError.Error(), want.MCPError.Error())
	}
	if got.MCPError.Error() != wantMessage {
		t.Fatalf("unexpected unsupported-surface MCP envelope semantics: got=%q want=%q", got.MCPError.Error(), wantMessage)
	}
}
