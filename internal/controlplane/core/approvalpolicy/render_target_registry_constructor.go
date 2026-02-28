package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"

// newDecideApprovalRenderTargetRegistry builds the decide-approval
// render-target registry from explicit surface→target intent.
func newDecideApprovalRenderTargetRegistry(targets map[DecideApprovalRenderSurface]DecideApprovalRenderTarget) projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderTarget] {
	return projectiondispatch.NewPolicyRegistry(targets)
}
