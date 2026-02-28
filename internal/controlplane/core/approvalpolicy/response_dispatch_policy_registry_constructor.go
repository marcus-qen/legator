package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// newDecideApprovalResponseDispatchPolicyRegistry builds the decide-approval
// projection policy registry from explicit surface→policy intent declared by
// transport adapters.
func newDecideApprovalResponseDispatchPolicyRegistry(policies map[DecideApprovalRenderSurface]decideApprovalResponseDispatchPolicy) projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, decideApprovalResponseDispatchPolicy] {
	return projectiondispatch.NewPolicyRegistry(policies)
}

func newDefaultDecideApprovalResponseDispatchPolicyRegistry() projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, decideApprovalResponseDispatchPolicy] {
	return projectiondispatch.NewHTTPMCPIdentityPolicyRegistry(
		DecideApprovalRenderSurfaceHTTP,
		decideApprovalResponseDispatchPolicy(projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](dispatchDecideApprovalResponseHTTP)),
		DecideApprovalRenderSurfaceMCP,
		decideApprovalResponseDispatchPolicy(projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](dispatchDecideApprovalResponseMCP)),
	)
}
