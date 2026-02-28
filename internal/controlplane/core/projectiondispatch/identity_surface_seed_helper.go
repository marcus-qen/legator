package projectiondispatch

// NewHTTPMCPIdentitySurfaceSeed constructs the canonical identity resolver
// seed for paired HTTP/MCP surface domains.
func NewHTTPMCPIdentitySurfaceSeed[Surface comparable](httpSurface, mcpSurface Surface) map[Surface]Surface {
	return map[Surface]Surface{
		httpSurface: httpSurface,
		mcpSurface:  mcpSurface,
	}
}
