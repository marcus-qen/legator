package approvalpolicy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestDispatchUnsupportedDecideApprovalSurfaceFallback_ParityWithLegacyRepeatedCallShape(t *testing.T) {
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
			newCapture := approvalUnsupportedFallbackCapture{}
			legacyCapture := approvalUnsupportedFallbackCapture{}

			dispatchUnsupportedDecideApprovalSurfaceFallback(surface, newCapture.writer(tt.withHTTP, tt.withMCP))
			legacyDispatchUnsupportedDecideApprovalSurfaceFallbackRepeatedCallShape(surface, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if !newCapture.equal(legacyCapture) {
				t.Fatalf("fallback dispatch parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

func legacyDispatchUnsupportedDecideApprovalSurfaceFallbackRepeatedCallShape(surface string, writer DecideApprovalResponseDispatchWriter) {
	transportwriter.WriteUnsupportedSurfaceFallback(
		unsupportedDecideApprovalSurfaceEnvelope(surface),
		newDecideApprovalUnsupportedSurfaceFallbackWriter(writer),
	)
}
