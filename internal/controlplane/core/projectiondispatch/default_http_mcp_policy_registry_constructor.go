package projectiondispatch

// NewHTTPMCPDefaultPolicyRegistry constructs canonical default HTTP/MCP
// policy-registry wiring by adapting dispatch functions to policies for paired
// surface domains.
func NewHTTPMCPDefaultPolicyRegistry[Surface comparable, Projection any, Writer any](
	httpSurface Surface,
	dispatchHTTP func(projection Projection, writer Writer),
	mcpSurface Surface,
	dispatchMCP func(projection Projection, writer Writer),
) PolicyRegistry[Surface, Policy[Projection, Writer]] {
	return NewHTTPMCPIdentityPolicyRegistry[Surface, Policy[Projection, Writer]](
		httpSurface,
		PolicyFunc[Projection, Writer](dispatchHTTP),
		mcpSurface,
		PolicyFunc[Projection, Writer](dispatchMCP),
	)
}
