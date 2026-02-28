package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// newCommandInvokeProjectionDispatchPolicyRegistry builds the command-invoke
// render-dispatch policy registry from explicit surface→policy intent.
func newCommandInvokeProjectionDispatchPolicyRegistry(policies map[ProjectionDispatchSurface]commandInvokeProjectionDispatchPolicy) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandInvokeProjectionDispatchPolicy] {
	return projectiondispatch.NewPolicyRegistry(policies)
}
