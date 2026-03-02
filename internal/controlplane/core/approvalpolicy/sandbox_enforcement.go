package approvalpolicy

import (
	"strings"

	"github.com/marcus-qen/legator/internal/protocol"
)

func enforceSandboxLaneForMutation(decision CommandPolicyDecision, cmd *protocol.CommandPayload) CommandPolicyDecision {
	if !isMutationDecision(decision, cmd) {
		return decision
	}

	switch decision.Lane {
	case protocol.ExecObserveDirect:
		return forceDeniedDecision(decision, "policy.host_direct_mutation_blocked")
	case protocol.ExecBreakglassDirect:
		if reasonCode := validateBreakglassInvocation(decision.Policy.Breakglass, cmd); reasonCode != "" {
			return forceDeniedDecision(decision, reasonCode)
		}
	}

	return decision
}

func isMutationDecision(decision CommandPolicyDecision, cmd *protocol.CommandPayload) bool {
	if decision.Classification.Capability == protocol.CapRemediate {
		return true
	}
	if cmd == nil {
		return false
	}
	classification := classifyCommandWithMetadata(cmd.Command, cmd.Args)
	return classification.Level == protocol.CapRemediate
}

func validateBreakglassInvocation(policy protocol.BreakglassPolicy, cmd *protocol.CommandPayload) string {
	if !policy.Enabled {
		return "policy.breakglass_disabled"
	}
	if cmd == nil || cmd.Breakglass == nil {
		return "policy.breakglass_required"
	}

	reason := strings.TrimSpace(cmd.Breakglass.Reason)
	if reason == "" {
		return "policy.breakglass_reason_required"
	}
	if len(policy.AllowedReasons) > 0 {
		matched := false
		for _, allowed := range policy.AllowedReasons {
			if strings.EqualFold(strings.TrimSpace(allowed), reason) {
				matched = true
				break
			}
		}
		if !matched {
			return "policy.breakglass_reason_not_allowed"
		}
	}
	if policy.RequireTypedConfirmation {
		if strings.TrimSpace(cmd.Breakglass.TypedConfirmation) != protocol.BreakglassTypedConfirmationPhrase {
			return "policy.breakglass_confirmation_required"
		}
	}

	return ""
}

func forceDeniedDecision(decision CommandPolicyDecision, reasonCode string) CommandPolicyDecision {
	decision = mergeDecisionWithReason(decision, CommandPolicyDecisionDeny, reasonCode)
	decision.GateOutcome = CommandPolicyGateBlocked
	decision.ReasonCode = reasonCode
	if strings.TrimSpace(decision.Rationale.Summary) == "" {
		decision.Rationale.Summary = "command denied by sandbox lane enforcement"
	}
	return decision
}
