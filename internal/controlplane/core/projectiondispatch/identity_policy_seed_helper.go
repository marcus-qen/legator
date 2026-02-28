package projectiondispatch

// NewHTTPMCPIdentityPolicySeed constructs the canonical HTTP/MCP identity
// policy seed map for paired surface domains.
func NewHTTPMCPIdentityPolicySeed[Surface comparable, Policy any](httpSurface Surface, httpPolicy Policy, mcpSurface Surface, mcpPolicy Policy) map[Surface]Policy {
	return map[Surface]Policy{
		httpSurface: httpPolicy,
		mcpSurface:  mcpPolicy,
	}
}
