package transportwriter

import "fmt"

// UnsupportedSurfaceScope identifies the domain operation used in
// unsupported-surface fallback messages.
type UnsupportedSurfaceScope string

const (
	UnsupportedSurfaceScopeApprovalDecideDispatch UnsupportedSurfaceScope = "approval decide dispatch"
	UnsupportedSurfaceScopeCommandInvoke          UnsupportedSurfaceScope = "command invoke"
	UnsupportedSurfaceScopeCommandDispatch        UnsupportedSurfaceScope = "command dispatch"
)

// UnsupportedSurfaceMessage formats the shared unsupported-surface message
// contract consumed by approval/command codecs and adapters.
func UnsupportedSurfaceMessage(scope UnsupportedSurfaceScope, surface string) string {
	return fmt.Sprintf("unsupported %s surface %q", scope, surface)
}
