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

func TestUnsupportedDecideApprovalResponseEnvelope_FallbackParity(t *testing.T) {
	const wantMessage = "unsupported approval decide dispatch surface \"bogus\""
	envelope := unsupportedDecideApprovalResponseEnvelope("bogus")

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
