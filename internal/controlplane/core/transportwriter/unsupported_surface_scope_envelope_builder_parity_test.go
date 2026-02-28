package transportwriter

import "testing"

func TestUnsupportedSurfaceEnvelopeBuilderForScope_ParityWithLegacyScopeToEnvelopeWiring(t *testing.T) {
	tests := []struct {
		name    string
		scope   UnsupportedSurfaceScope
		surface string
	}{
		{name: "approval decide dispatch", scope: UnsupportedSurfaceScopeApprovalDecideDispatch, surface: "bogus"},
		{name: "command invoke", scope: UnsupportedSurfaceScopeCommandInvoke, surface: "bogus"},
		{name: "command dispatch", scope: UnsupportedSurfaceScopeCommandDispatch, surface: "bogus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buildEnvelope := UnsupportedSurfaceEnvelopeBuilderForScope(tt.scope)

			got := buildEnvelope(tt.surface)
			want := legacyUnsupportedSurfaceEnvelopeForScope(tt.scope, tt.surface)

			assertUnsupportedSurfaceEnvelopeParity(t, got, want)
		})
	}
}

func legacyUnsupportedSurfaceEnvelopeForScope(scope UnsupportedSurfaceScope, surface string) *ResponseEnvelope {
	return UnsupportedSurfaceEnvelope(UnsupportedSurfaceMessage(scope, surface))
}

func assertUnsupportedSurfaceEnvelopeParity(t *testing.T, got, want *ResponseEnvelope) {
	t.Helper()
	if got == nil || want == nil {
		t.Fatalf("expected non-nil envelopes, got=%+v want=%+v", got, want)
	}
	if got.HTTPError == nil || want.HTTPError == nil {
		t.Fatalf("expected non-nil HTTP errors, got=%+v want=%+v", got.HTTPError, want.HTTPError)
	}
	if got.HTTPError.Status != want.HTTPError.Status || got.HTTPError.Code != want.HTTPError.Code || got.HTTPError.Message != want.HTTPError.Message {
		t.Fatalf("HTTP envelope mismatch: got=%+v want=%+v", got.HTTPError, want.HTTPError)
	}
	if got.MCPError == nil || want.MCPError == nil {
		t.Fatalf("expected non-nil MCP errors, got=%v want=%v", got.MCPError, want.MCPError)
	}
	if got.MCPError.Error() != want.MCPError.Error() {
		t.Fatalf("MCP envelope mismatch: got=%q want=%q", got.MCPError.Error(), want.MCPError.Error())
	}
}
