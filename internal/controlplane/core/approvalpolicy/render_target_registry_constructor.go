package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// newDecideApprovalRenderSurfaceRegistry builds the decide-approval resolver
// hook registry from explicit surface→surface intent.
func newDecideApprovalRenderSurfaceRegistry(surfaces map[DecideApprovalRenderSurface]DecideApprovalRenderSurface) projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderSurface] {
	return projectiondispatch.NewIdentitySurfaceRegistry(surfaces)
}

// newDefaultDecideApprovalRenderSurfaceRegistry builds the canonical
// HTTP/MCP decide-approval resolver hook registry.
func newDefaultDecideApprovalRenderSurfaceRegistry() projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderSurface] {
	return projectiondispatch.NewHTTPMCPIdentitySurfaceRegistry(
		DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP,
	)
}
