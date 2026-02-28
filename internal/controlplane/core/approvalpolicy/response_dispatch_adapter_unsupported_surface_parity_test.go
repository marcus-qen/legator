package approvalpolicy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestDispatchDecideApprovalResponseForSurface_UnsupportedSurfaceAdapterFallbackParityWithLegacyInlineBranch(t *testing.T) {
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

	projection := ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{}, nil))
	surface := DecideApprovalRenderSurface("bogus")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := approvalUnsupportedFallbackCapture{}
			legacyCapture := approvalUnsupportedFallbackCapture{}

			DispatchDecideApprovalResponseForSurface(projection, surface, newCapture.writer(tt.withHTTP, tt.withMCP))
			legacyDispatchDecideApprovalResponseForSurfaceUnsupportedInlineBranch(projection, surface, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if !newCapture.equal(legacyCapture) {
				t.Fatalf("unsupported-surface adapter parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
			}
		})
	}
}

func legacyDispatchDecideApprovalResponseForSurfaceUnsupportedInlineBranch(projection *DecideApprovalProjection, surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
	builder := DecideApprovalResponseEnvelopeBuilder{Projection: projection}
	transportSurface, ok := ResolveDecideApprovalTransportSurface(surface)
	if !ok {
		dispatchUnsupportedDecideApprovalSurfaceFallback(surface, writer)
		return
	}

	transportwriter.WriteFromBuilder(transportSurface, builder, newDecideApprovalWriterKernel(writer))
}
