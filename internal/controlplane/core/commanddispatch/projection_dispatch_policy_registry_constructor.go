package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// newCommandDispatchErrorPolicyRegistry builds the command-dispatch error
// policy registry from explicit surface→policy intent.
func newCommandDispatchErrorPolicyRegistry(policies map[ProjectionDispatchSurface]commandDispatchErrorPolicy) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandDispatchErrorPolicy] {
	return projectiondispatch.NewPolicyRegistry(policies)
}

// newCommandReadPolicyRegistry builds the command-read projection policy
// registry from explicit surface→policy intent.
func newCommandReadPolicyRegistry(policies map[ProjectionDispatchSurface]commandReadPolicy) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandReadPolicy] {
	return projectiondispatch.NewPolicyRegistry(policies)
}

func newDefaultCommandDispatchErrorPolicyRegistry() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandDispatchErrorPolicy] {
	return projectiondispatch.NewHTTPMCPDefaultPolicyRegistry(
		ProjectionDispatchSurfaceHTTP,
		dispatchCommandEnvelopeHTTPError,
		ProjectionDispatchSurfaceMCP,
		dispatchCommandEnvelopeMCPError,
	)
}

func newDefaultCommandReadPolicyRegistry() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandReadPolicy] {
	return projectiondispatch.NewHTTPMCPDefaultPolicyRegistry(
		ProjectionDispatchSurfaceHTTP,
		dispatchCommandReadHTTP,
		ProjectionDispatchSurfaceMCP,
		dispatchCommandReadMCP,
	)
}
