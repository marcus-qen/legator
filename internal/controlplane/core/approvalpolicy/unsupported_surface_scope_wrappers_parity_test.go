package approvalpolicy

import (
	"net/http"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestUnsupportedDecideApprovalSurfaceMessage_ParityWithTransportScope(t *testing.T) {
	const surface = "bogus"
	got := unsupportedDecideApprovalSurfaceMessage(surface)
	want := transportwriter.UnsupportedSurfaceMessage(transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch, surface)
	if got != want {
		t.Fatalf("unsupported-surface message parity mismatch: got %q want %q", got, want)
	}
}

func TestUnsupportedDecideApprovalSurfaceEnvelope_ParityWithTransportFactory(t *testing.T) {
	const surface = "bogus"
	message := transportwriter.UnsupportedSurfaceMessage(transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch, surface)

	got := unsupportedDecideApprovalSurfaceEnvelope(surface)
	want := transportwriter.UnsupportedSurfaceEnvelope(message)
	assertUnsupportedSurfaceEnvelopeParity(t, got, want, message)
}

func TestUnsupportedDecideApprovalResponseEnvelope_FallbackParity(t *testing.T) {
	const surface = "bogus"
	const wantMessage = "unsupported approval decide dispatch surface \"bogus\""
	envelope := unsupportedDecideApprovalResponseEnvelope(surface)
	assertUnsupportedSurfaceEnvelopeParity(t, envelope, unsupportedDecideApprovalSurfaceEnvelope(surface), wantMessage)

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

func TestDispatchUnsupportedDecideApprovalSurfaceFallback_ParityAndPrecedence(t *testing.T) {
	const surface = "bogus"
	const wantMessage = "unsupported approval decide dispatch surface \"bogus\""

	httpCalled, mcpCalled := false, false
	dispatchUnsupportedDecideApprovalSurfaceFallback(surface, DecideApprovalResponseDispatchWriter{
		WriteHTTPError: func(err *HTTPErrorContract) {
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
	dispatchUnsupportedDecideApprovalSurfaceFallback(surface, DecideApprovalResponseDispatchWriter{
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
