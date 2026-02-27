package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

const unsupportedDecideApprovalScope = transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch

func unsupportedDecideApprovalSurfaceMessage(surface string) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedDecideApprovalScope, surface)
}
