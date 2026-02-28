package projectiondispatch

// NewIdentitySurfaceRegistry constructs a surface resolver registry where
// supported surfaces resolve to themselves.
func NewIdentitySurfaceRegistry[Surface comparable](surfaces map[Surface]Surface) PolicyRegistry[Surface, Surface] {
	return NewPolicyRegistry(surfaces)
}

// NewHTTPMCPIdentitySurfaceRegistry constructs the canonical HTTP/MCP identity
// resolver registry for paired surface domains.
func NewHTTPMCPIdentitySurfaceRegistry[Surface comparable](httpSurface, mcpSurface Surface) PolicyRegistry[Surface, Surface] {
	return NewIdentitySurfaceRegistry(NewHTTPMCPIdentitySurfaceSeed(httpSurface, mcpSurface))
}
