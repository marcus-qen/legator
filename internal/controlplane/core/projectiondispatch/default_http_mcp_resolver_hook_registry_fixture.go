package projectiondispatch

// NewHTTPMCPDefaultResolverHookRegistryFixture constructs the canonical
// default resolver-hook surface registry fixture for string-backed HTTP/MCP
// surface enums used by approval + command parity tests.
func NewHTTPMCPDefaultResolverHookRegistryFixture[Surface ~string]() PolicyRegistry[Surface, Surface] {
	return NewHTTPMCPDefaultIdentitySurfaceRegistry(
		Surface("http"),
		Surface("mcp"),
	)
}
