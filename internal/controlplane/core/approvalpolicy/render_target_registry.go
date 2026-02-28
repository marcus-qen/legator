package approvalpolicy

import (
	"io"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

// DecideApprovalRenderSurface identifies a transport shell that consumes decide
// projections and is mapped to a render target via the shared registry.
type DecideApprovalRenderSurface string

const (
	DecideApprovalRenderSurfaceHTTP DecideApprovalRenderSurface = "http"
	DecideApprovalRenderSurfaceMCP  DecideApprovalRenderSurface = "mcp"
)

var defaultDecideApprovalRenderSurfaceRegistry = newDecideApprovalRenderSurfaceRegistry(map[DecideApprovalRenderSurface]DecideApprovalRenderSurface{
	DecideApprovalRenderSurfaceHTTP: DecideApprovalRenderSurfaceHTTP,
	DecideApprovalRenderSurfaceMCP:  DecideApprovalRenderSurfaceMCP,
})

// ResolveDecideApprovalRenderTarget resolves the projection render target for a
// transport shell using the shared render-target registry.
func ResolveDecideApprovalRenderTarget(surface DecideApprovalRenderSurface) (DecideApprovalRenderTarget, bool) {
	resolvedSurface, ok := defaultDecideApprovalRenderSurfaceRegistry.Resolve(surface)
	if !ok {
		return "", false
	}
	return DecideApprovalRenderTarget(resolvedSurface), true
}

// ResolveDecideApprovalTransportSurface resolves a decide surface to the
// shared transportwriter surface via the shared resolver seam.
func ResolveDecideApprovalTransportSurface(surface DecideApprovalRenderSurface) (transportwriter.Surface, bool) {
	return transportwriter.ResolveSurfaceToTransport(defaultDecideApprovalRenderSurfaceRegistry, surface)
}

// OrchestrateDecideApprovalForSurface runs the shared decide orchestration after
// resolving the shell render target through the shared registry boundary.
func OrchestrateDecideApprovalForSurface(body io.Reader, decide func(*DecideApprovalRequest) (*ApprovalDecisionResult, error), surface DecideApprovalRenderSurface) *DecideApprovalProjection {
	target, ok := ResolveDecideApprovalRenderTarget(surface)
	if !ok {
		return SelectDecideApprovalProjection(&DecideApprovalTransportContract{}, DecideApprovalRenderTarget(surface))
	}
	return OrchestrateDecideApproval(body, decide, target)
}
