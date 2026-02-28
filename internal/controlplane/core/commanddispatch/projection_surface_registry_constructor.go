package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// newCommandDispatchProjectionSurfaceRegistry builds the command-dispatch
// resolver hook registry from explicit surface→surface intent.
func newCommandDispatchProjectionSurfaceRegistry(surfaces map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewIdentitySurfaceRegistry(surfaces)
}

// newCommandReadProjectionSurfaceRegistry builds the command-read resolver
// hook registry from explicit surface→surface intent.
func newCommandReadProjectionSurfaceRegistry(surfaces map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewIdentitySurfaceRegistry(surfaces)
}

// newCommandInvokeProjectionSurfaceRegistry builds the command-invoke resolver
// hook registry from explicit surface→surface intent.
func newCommandInvokeProjectionSurfaceRegistry(surfaces map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewIdentitySurfaceRegistry(surfaces)
}

// newDefaultCommandDispatchProjectionSurfaceRegistry builds the canonical
// HTTP/MCP command-dispatch resolver hook registry.
func newDefaultCommandDispatchProjectionSurfaceRegistry() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewHTTPMCPIdentitySurfaceRegistry(
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
	)
}

// newDefaultCommandReadProjectionSurfaceRegistry builds the canonical HTTP/MCP
// command-read resolver hook registry.
func newDefaultCommandReadProjectionSurfaceRegistry() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewHTTPMCPIdentitySurfaceRegistry(
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
	)
}

// newDefaultCommandInvokeProjectionSurfaceRegistry builds the canonical
// HTTP/MCP command-invoke resolver hook registry.
func newDefaultCommandInvokeProjectionSurfaceRegistry() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewHTTPMCPIdentitySurfaceRegistry(
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
	)
}
