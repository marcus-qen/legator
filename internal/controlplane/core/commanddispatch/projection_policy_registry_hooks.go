package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

// ProjectionDispatchSurface identifies transport shells expected to consume
// command-dispatch and command-read projections in upcoming kernel splits.
type ProjectionDispatchSurface string

const (
	ProjectionDispatchSurfaceHTTP ProjectionDispatchSurface = "http"
	ProjectionDispatchSurfaceMCP  ProjectionDispatchSurface = "mcp"
)

var (
	defaultCommandDispatchProjectionSurfaceRegistry = newCommandDispatchProjectionSurfaceRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})
	defaultCommandReadProjectionSurfaceRegistry = newCommandReadProjectionSurfaceRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})
	defaultCommandInvokeProjectionDispatchSurfaceRegistry = newCommandInvokeProjectionSurfaceRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
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

// ResolveCommandInvokeTransportSurface resolves a command invoke surface to the
// shared transportwriter surface via the shared resolver seam.
func ResolveCommandInvokeTransportSurface(surface ProjectionDispatchSurface) (transportwriter.Surface, bool) {
	return transportwriter.ResolveSurfaceToTransport(defaultCommandInvokeProjectionDispatchSurfaceRegistry, surface)
}
