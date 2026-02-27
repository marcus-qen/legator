package approvalpolicy

import (
	"io"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

// DecideApprovalRenderSurface identifies a transport shell that consumes decide
// projections and is mapped to a render target via the shared registry.
type DecideApprovalRenderSurface string

const (
	DecideApprovalRenderSurfaceHTTP DecideApprovalRenderSurface = "http"
	DecideApprovalRenderSurfaceMCP  DecideApprovalRenderSurface = "mcp"
)

var defaultDecideApprovalRenderTargetRegistry = projectiondispatch.NewPolicyRegistry(map[DecideApprovalRenderSurface]DecideApprovalRenderTarget{
	DecideApprovalRenderSurfaceHTTP: DecideApprovalRenderTargetHTTP,
	DecideApprovalRenderSurfaceMCP:  DecideApprovalRenderTargetMCP,
})

// ResolveDecideApprovalRenderTarget resolves the projection render target for a
// transport shell using the shared render-target registry.
func ResolveDecideApprovalRenderTarget(surface DecideApprovalRenderSurface) (DecideApprovalRenderTarget, bool) {
	return defaultDecideApprovalRenderTargetRegistry.Resolve(surface)
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
