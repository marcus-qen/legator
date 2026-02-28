package projectiondispatch

// NewHTTPMCPIdentityPolicyRegistry constructs the canonical HTTP/MCP identity
// policy registry for paired surface domains.
func NewHTTPMCPIdentityPolicyRegistry[Surface comparable, Policy any](httpSurface Surface, httpPolicy Policy, mcpSurface Surface, mcpPolicy Policy) PolicyRegistry[Surface, Policy] {
	return NewPolicyRegistry(NewHTTPMCPIdentityPolicySeed(httpSurface, httpPolicy, mcpSurface, mcpPolicy))
}
