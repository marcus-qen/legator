package projectiondispatch

// NewHTTPMCPDefaultIdentitySurfaceRegistryFixture constructs canonical default
// resolver-hook registry fixture wiring by applying a domain constructor to the
// shared HTTP/MCP identity surface seed.
func NewHTTPMCPDefaultIdentitySurfaceRegistryFixture[Surface comparable](
	constructor func(map[Surface]Surface) PolicyRegistry[Surface, Surface],
	httpSurface Surface,
	mcpSurface Surface,
) PolicyRegistry[Surface, Surface] {
	return constructor(NewHTTPMCPIdentitySurfaceSeed(httpSurface, mcpSurface))
}
