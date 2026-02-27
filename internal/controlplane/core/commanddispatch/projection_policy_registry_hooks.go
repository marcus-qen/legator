package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// ProjectionDispatchSurface identifies transport shells expected to consume
// command-dispatch and command-read projections in upcoming kernel splits.
type ProjectionDispatchSurface string

const (
	ProjectionDispatchSurfaceHTTP ProjectionDispatchSurface = "http"
	ProjectionDispatchSurfaceMCP  ProjectionDispatchSurface = "mcp"
)

var (
	defaultCommandDispatchProjectionSurfaceRegistry = projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})
	defaultCommandReadProjectionSurfaceRegistry = projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})
	defaultCommandInvokeProjectionDispatchSurfaceRegistry = projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})
)

// ResolveCommandDispatchProjectionSurface is an extension hook for future
// command-dispatch projection adapter extraction.
func ResolveCommandDispatchProjectionSurface(surface ProjectionDispatchSurface) (ProjectionDispatchSurface, bool) {
	return defaultCommandDispatchProjectionSurfaceRegistry.Resolve(surface)
}

// ResolveCommandReadProjectionSurface is an extension hook for future
// command-read projection adapter extraction.
func ResolveCommandReadProjectionSurface(surface ProjectionDispatchSurface) (ProjectionDispatchSurface, bool) {
	return defaultCommandReadProjectionSurfaceRegistry.Resolve(surface)
}

// ResolveCommandInvokeProjectionDispatchSurface is an extension hook for
// command invoke render-dispatch adapter surface selection.
func ResolveCommandInvokeProjectionDispatchSurface(surface ProjectionDispatchSurface) (ProjectionDispatchSurface, bool) {
	return defaultCommandInvokeProjectionDispatchSurfaceRegistry.Resolve(surface)
}
