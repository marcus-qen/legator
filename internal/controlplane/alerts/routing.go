package alerts

import (
	"strings"
	"time"
)

// Severity constants for alert routing policy matchers.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// RoutingPolicy defines how alerts are routed to owners/teams.
type RoutingPolicy struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	Description        string           `json:"description,omitempty"`
	// Priority determines precedence among matching policies; higher wins.
	Priority           int              `json:"priority"`
	// IsDefault marks this as the fallback policy when no matchers hit.
	IsDefault          bool             `json:"is_default"`
	// Matchers are AND-ed together; empty slice = matches every alert.
	Matchers           []RoutingMatcher `json:"matchers"`
	// Owner fields
	OwnerLabel         string           `json:"owner_label"`
	OwnerContact       string           `json:"owner_contact,omitempty"`
	// Optional reference to an EscalationPolicy
	EscalationPolicyID string           `json:"escalation_policy_id,omitempty"`
	// Runbook URL for this ownership domain
	RunbookURL         string           `json:"runbook_url,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

// RoutingMatcher is a single matching criterion for a routing policy.
type RoutingMatcher struct {
	// Field: "severity", "condition_type", "rule_name", "tag"
	Field string `json:"field"`
	// Op: "eq" (default), "contains", "prefix"
	Op    string `json:"op"`
	Value string `json:"value"`
}

// EscalationPolicy defines an ordered escalation chain.
type EscalationPolicy struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Steps       []EscalationStep `json:"steps"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// EscalationStep is one step in an escalation chain.
type EscalationStep struct {
	// Order is 1-based; steps are executed in ascending order.
	Order       int    `json:"order"`
	// Target identifies who/what to notify.
	Target      string `json:"target"`
	// TargetType: "email", "webhook", "team", "oncall"
	TargetType  string `json:"target_type"`
	// DelayMin: minutes after alert fires before this step activates.
	DelayMin    int    `json:"delay_minutes"`
	// RunbookURL overrides the policy-level runbook for this step.
	RunbookURL  string `json:"runbook_url,omitempty"`
	Description string `json:"description,omitempty"`
}

// RoutingContext is the input for resolving which routing policy applies.
type RoutingContext struct {
	RuleID        string   `json:"rule_id"`
	RuleName      string   `json:"rule_name"`
	ConditionType string   `json:"condition_type"`
	Severity      string   `json:"severity,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	ProbeID       string   `json:"probe_id,omitempty"`
}

// RoutingOutcome is the result of routing resolution for an alert.
type RoutingOutcome struct {
	RuleID             string           `json:"rule_id"`
	ProbeID            string           `json:"probe_id,omitempty"`
	PolicyID           string           `json:"policy_id"`
	PolicyName         string           `json:"policy_name"`
	OwnerLabel         string           `json:"owner_label"`
	OwnerContact       string           `json:"owner_contact,omitempty"`
	RunbookURL         string           `json:"runbook_url,omitempty"`
	EscalationPolicyID string           `json:"escalation_policy_id,omitempty"`
	EscalationSteps    []EscalationStep `json:"escalation_steps,omitempty"`
	Explain            RoutingExplain   `json:"explain"`
}

// RoutingExplain describes why a particular routing policy was selected.
type RoutingExplain struct {
	// MatchedBy describes which matcher/criterion caused selection.
	MatchedBy    string `json:"matched_by"`
	// FallbackUsed is true when the default policy was used.
	FallbackUsed bool   `json:"fallback_used"`
	// Reason is a human-readable explanation.
	Reason       string `json:"reason"`
}

// DeliveredAlertEvent wraps an AlertEvent with resolved routing context.
// It is emitted on the event bus and delivered to webhooks in place of a bare AlertEvent.
// Existing consumers that only read AlertEvent fields are unaffected (additive).
type DeliveredAlertEvent struct {
	AlertEvent
	Routing *RoutingOutcome `json:"routing,omitempty"`
}

// -------------------------------------------------------------------
// Matcher logic
// -------------------------------------------------------------------

// policyMatches returns true if every matcher in the policy matches ctx.
// A policy with zero matchers matches everything (wildcard).
func policyMatches(policy RoutingPolicy, ctx RoutingContext) bool {
	if len(policy.Matchers) == 0 {
		return true
	}
	for _, m := range policy.Matchers {
		if !matcherMatches(m, ctx) {
			return false
		}
	}
	return true
}

// matcherMatches evaluates a single RoutingMatcher against a RoutingContext.
func matcherMatches(m RoutingMatcher, ctx RoutingContext) bool {
	field := strings.ToLower(strings.TrimSpace(m.Field))
	switch field {
	case "severity":
		return matchOp(m.Op, ctx.Severity, m.Value)
	case "condition_type":
		return matchOp(m.Op, ctx.ConditionType, m.Value)
	case "rule_name":
		return matchOp(m.Op, ctx.RuleName, m.Value)
	case "tag":
		return matchTagField(m.Op, m.Value, ctx.Tags)
	default:
		return false
	}
}

func matchTagField(op, value string, tags []string) bool {
	for _, tag := range tags {
		if matchOp(op, tag, value) {
			return true
		}
	}
	return false
}

func matchOp(op, candidate, value string) bool {
	c := strings.ToLower(strings.TrimSpace(candidate))
	v := strings.ToLower(strings.TrimSpace(value))
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "", "eq":
		return c == v
	case "contains":
		return strings.Contains(c, v)
	case "prefix":
		return strings.HasPrefix(c, v)
	default:
		return false
	}
}

// describeMatcher returns a short string summarising why a policy matched.
func describeMatcher(p RoutingPolicy, ctx RoutingContext) string {
	if len(p.Matchers) == 0 {
		return "wildcard (no matchers)"
	}
	parts := make([]string, 0, len(p.Matchers))
	for _, m := range p.Matchers {
		parts = append(parts, m.Field+"="+m.Value)
	}
	return strings.Join(parts, ", ")
}
