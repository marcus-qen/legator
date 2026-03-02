package policy

import (
	"strings"

	"github.com/marcus-qen/legator/internal/protocol"
)

const (
	BreakglassConfirmReasonField = "breakglass_reason"
	BreakglassConfirmTokenField  = "breakglass_token"
)

// BreakglassConfirmation captures explicit operator confirmation supplied with
// mutation requests that require a direct host lane.
type BreakglassConfirmation struct {
	Confirmed bool   `json:"confirmed"`
	Method    string `json:"method,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// IsHostDirectLane returns true when the selected execution lane runs directly
// on the host (no sandbox isolation).
func IsHostDirectLane(lane protocol.ExecutionClass) bool {
	switch lane {
	case protocol.ExecObserveDirect, protocol.ExecBreakglassDirect:
		return true
	default:
		return false
	}
}

// IsMutationCategory returns true when classification indicates a mutating
// command/action.
func IsMutationCategory(category string) bool {
	return strings.EqualFold(strings.TrimSpace(category), "mutation")
}

// RequiresBreakglassConfirmation indicates whether sandbox enforcement requires
// explicit breakglass confirmation for the selected lane.
func RequiresBreakglassConfirmation(category string, lane protocol.ExecutionClass) bool {
	return IsMutationCategory(category) && IsHostDirectLane(lane)
}

// ResolveBreakglassConfirmation accepts either breakglass_reason or
// breakglass_token as explicit typed confirmation.
func ResolveBreakglassConfirmation(reason, token string) BreakglassConfirmation {
	reason = strings.TrimSpace(reason)
	if reason != "" {
		return BreakglassConfirmation{
			Confirmed: true,
			Method:    BreakglassConfirmReasonField,
			Reason:    reason,
		}
	}
	token = strings.TrimSpace(token)
	if token != "" {
		return BreakglassConfirmation{
			Confirmed: true,
			Method:    BreakglassConfirmTokenField,
			Reason:    token,
		}
	}
	return BreakglassConfirmation{}
}

// BreakglassReasonAllowed validates a reason against the policy allow-list.
// Empty allow-lists are treated as unrestricted.
func BreakglassReasonAllowed(reason string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return false
	}
	for _, candidate := range allowed {
		if reason == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}
