package transportwriter

import "fmt"

// UnsupportedSurfaceMessage formats the shared unsupported-surface message
// contract consumed by approval/command codecs and adapters.
func UnsupportedSurfaceMessage(scope string, surface string) string {
	return fmt.Sprintf("unsupported %s surface %q", scope, surface)
}
