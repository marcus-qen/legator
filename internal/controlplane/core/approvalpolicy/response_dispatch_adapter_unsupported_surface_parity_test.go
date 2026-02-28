package approvalpolicy

import (
	"errors"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestDispatchDecideApprovalResponseForSurface_ResolveOrUnsupportedBranchParityWithLegacyInlineBranch(t *testing.T) {
	tests := []struct {
		name     string
		surface  DecideApprovalRenderSurface
		withHTTP bool
		withMCP  bool
	}{
		{name: "http resolved with both callbacks", surface: DecideApprovalRenderSurfaceHTTP, withHTTP: true, withMCP: true},
		{name: "mcp resolved with both callbacks", surface: DecideApprovalRenderSurfaceMCP, withHTTP: true, withMCP: true},
		{name: "unsupported with both callbacks", surface: DecideApprovalRenderSurface("bogus"), withHTTP: true, withMCP: true},
		{name: "unsupported with mcp callback", surface: DecideApprovalRenderSurface("bogus"), withHTTP: false, withMCP: true},
	}

	projection := ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(nil, &ApprovedDispatchError{Err: errors.New("probe p-adapter not connected")}))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := approvalUnsupportedFallbackCapture{}
			legacyCapture := approvalUnsupportedFallbackCapture{}

			DispatchDecideApprovalResponseForSurface(projection, tt.surface, newCapture.writer(tt.withHTTP, tt.withMCP))
			legacyDispatchDecideApprovalResponseForSurfaceUnsupportedInlineBranch(projection, tt.surface, legacyCapture.writer(tt.withHTTP, tt.withMCP))

			if !newCapture.equal(legacyCapture) {
				t.Fatalf("resolve-or-unsupported branch parity mismatch: new=%+v legacy=%+v", newCapture, legacyCapture)
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
