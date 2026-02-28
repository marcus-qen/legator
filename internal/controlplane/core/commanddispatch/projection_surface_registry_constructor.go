package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// newCommandDispatchProjectionSurfaceRegistry builds the command-dispatch
// resolver hook registry from explicit surface→surface intent.
func newCommandDispatchProjectionSurfaceRegistry(surfaces map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewPolicyRegistry(surfaces)
}

// newCommandReadProjectionSurfaceRegistry builds the command-read resolver
// hook registry from explicit surface→surface intent.
func newCommandReadProjectionSurfaceRegistry(surfaces map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewPolicyRegistry(surfaces)
}

// newCommandInvokeProjectionSurfaceRegistry builds the command-invoke resolver
// hook registry from explicit surface→surface intent.
func newCommandInvokeProjectionSurfaceRegistry(surfaces map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
	return projectiondispatch.NewPolicyRegistry(surfaces)
}
