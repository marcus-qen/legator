package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

const (
	unsupportedCommandInvokeScope   = transportwriter.UnsupportedSurfaceScopeCommandInvoke
	unsupportedCommandDispatchScope = transportwriter.UnsupportedSurfaceScopeCommandDispatch
)

func unsupportedCommandInvokeSurfaceMessage(surface string) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedCommandInvokeScope, surface)
}

func unsupportedCommandDispatchSurfaceMessage(surface ProjectionDispatchSurface) string {
	return transportwriter.UnsupportedSurfaceMessage(unsupportedCommandDispatchScope, string(surface))
}
