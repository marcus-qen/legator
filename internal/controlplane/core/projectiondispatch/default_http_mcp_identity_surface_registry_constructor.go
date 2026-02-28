package projectiondispatch

// NewHTTPMCPDefaultIdentitySurfaceRegistry constructs canonical default
// HTTP/MCP identity-surface registry wiring for resolver setup paths.
func NewHTTPMCPDefaultIdentitySurfaceRegistry[Surface comparable](
	httpSurface Surface,
	mcpSurface Surface,
) PolicyRegistry[Surface, Surface] {
	return NewHTTPMCPIdentitySurfaceRegistry(httpSurface, mcpSurface)
}
