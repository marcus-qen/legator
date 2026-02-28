package approvalpolicy

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/protocol"
)

// CommandPolicyDecisionOutcome captures the policy action for a command request.
type CommandPolicyDecisionOutcome string

const (
	CommandPolicyDecisionAllow CommandPolicyDecisionOutcome = "allow"
	CommandPolicyDecisionDeny  CommandPolicyDecisionOutcome = "deny"
	CommandPolicyDecisionQueue CommandPolicyDecisionOutcome = "queue"
)

const capacityPolicyVersion = "capacity-policy-v1"

// CapacityThresholds controls how Grafana capacity signals map to policy outcomes.
type CapacityThresholds struct {
	MinDatasourceCount   int
	MinDashboardCoverage float64
	MinQueryCoverage     float64
}

// DefaultCapacityThresholds returns the default policy thresholds.
func DefaultCapacityThresholds() CapacityThresholds {
	return CapacityThresholds{
		MinDatasourceCount:   1,
		MinDashboardCoverage: 0.50,
		MinQueryCoverage:     0.25,
	}
}

func (t CapacityThresholds) normalized() CapacityThresholds {
	n := t
	if n.MinDatasourceCount <= 0 {
		n.MinDatasourceCount = 1
	}
	if n.MinDashboardCoverage <= 0 || n.MinDashboardCoverage > 1 {
		n.MinDashboardCoverage = 0.50
	}
	if n.MinQueryCoverage <= 0 || n.MinQueryCoverage > 1 {
		n.MinQueryCoverage = 0.25
	}
	return n
}

// CapacitySignalProvider provides Grafana-derived capacity indicators to policy evaluation.
type CapacitySignalProvider interface {
	CapacitySignals(ctx context.Context) (*CapacitySignals, error)
}

// CapacitySignalProviderFunc adapts a function to CapacitySignalProvider.
type CapacitySignalProviderFunc func(ctx context.Context) (*CapacitySignals, error)

func (fn CapacitySignalProviderFunc) CapacitySignals(ctx context.Context) (*CapacitySignals, error) {
	if fn == nil {
		return nil, nil
	}
	return fn(ctx)
}

// CapacitySignals contains the policy-relevant subset of Grafana snapshot fields.
type CapacitySignals struct {
	Source            string   `json:"source,omitempty"`
	Availability      string   `json:"availability,omitempty"`
	DashboardCoverage float64  `json:"dashboard_coverage"`
	QueryCoverage     float64  `json:"query_coverage"`
	DatasourceCount   int      `json:"datasource_count"`
	Partial           bool     `json:"partial"`
	Warnings          []string `json:"warnings,omitempty"`
}

// CommandPolicyDecision is the normalized policy decision for a command.
type CommandPolicyDecision struct {
	Outcome   CommandPolicyDecisionOutcome `json:"outcome"`
	RiskLevel string                       `json:"risk_level"`
	Rationale CommandPolicyRationale       `json:"rationale"`
}

// CommandPolicyRationale is a machine-readable explanation for policy outcomes.
type CommandPolicyRationale struct {
	Policy     string                     `json:"policy"`
	Summary    string                     `json:"summary"`
	Fallback   bool                       `json:"fallback"`
	Indicators []CommandPolicyIndicator   `json:"indicators,omitempty"`
	Capacity   *CapacitySignals           `json:"capacity,omitempty"`
	Thresholds CapacityThresholdsSnapshot `json:"thresholds"`
}

// CapacityThresholdsSnapshot serializes thresholds used for a decision.
type CapacityThresholdsSnapshot struct {
	MinDatasourceCount   int     `json:"min_datasource_count"`
	MinDashboardCoverage float64 `json:"min_dashboard_coverage"`
	MinQueryCoverage     float64 `json:"min_query_coverage"`
}

// CommandPolicyIndicator captures one signal considered during evaluation.
type CommandPolicyIndicator struct {
	Name         string                       `json:"name"`
	Source       string                       `json:"source,omitempty"`
	Value        any                          `json:"value,omitempty"`
	Comparator   string                       `json:"comparator,omitempty"`
	Threshold    any                          `json:"threshold,omitempty"`
	Severity     string                       `json:"severity"`
	Effect       CommandPolicyDecisionOutcome `json:"effect,omitempty"`
	DroveOutcome bool                         `json:"drove_outcome"`
	Message      string                       `json:"message,omitempty"`
}

// EvaluateCommandPolicy evaluates risk + capacity signals and returns allow/deny/queue.
func (s *Service) EvaluateCommandPolicy(ctx context.Context, cmd *protocol.CommandPayload, probeLevel protocol.CapabilityLevel) CommandPolicyDecision {
	_ = probeLevel // reserved for future capability-level policy hooks.

	thresholds := s.capacityThresholds.normalized()
	risk := approval.ClassifyRisk(cmd)
	decision := CommandPolicyDecision{
		Outcome:   CommandPolicyDecisionAllow,
		RiskLevel: risk,
		Rationale: CommandPolicyRationale{
			Policy:   capacityPolicyVersion,
			Fallback: false,
			Thresholds: CapacityThresholdsSnapshot{
				MinDatasourceCount:   thresholds.MinDatasourceCount,
				MinDashboardCoverage: thresholds.MinDashboardCoverage,
				MinQueryCoverage:     thresholds.MinQueryCoverage,
			},
		},
	}

	riskIndicator := CommandPolicyIndicator{
		Name:     "command_risk",
		Source:   "risk_classifier",
		Value:    risk,
		Severity: "info",
		Message:  "command risk classification",
	}
	if risk == "high" || risk == "critical" {
		decision.Outcome = CommandPolicyDecisionQueue
		riskIndicator.Effect = CommandPolicyDecisionQueue
		riskIndicator.DroveOutcome = true
		riskIndicator.Severity = "warn"
		riskIndicator.Message = "high-risk command requires human approval"
	}
	decision.Rationale.Indicators = append(decision.Rationale.Indicators, riskIndicator)

	signals, err := s.readCapacitySignals(ctx)
	if err != nil || signals == nil {
		decision.Rationale.Fallback = true
		indicator := CommandPolicyIndicator{
			Name:         "capacity_signals",
			Source:       "grafana_adapter",
			Value:        "unavailable",
			Severity:     "info",
			DroveOutcome: false,
			Message:      "Grafana capacity signals unavailable; using risk-only fallback",
		}
		if err != nil {
			indicator.Value = err.Error()
		}
		decision.Rationale.Indicators = append(decision.Rationale.Indicators, indicator)
		decision.Rationale.Summary = summarizeDecision(decision.Outcome, decision.Rationale.Indicators)
		return decision
	}

	capacity := *signals
	capacity.Warnings = cloneStrings(capacity.Warnings)
	decision.Rationale.Capacity = &capacity

	availability := strings.ToLower(strings.TrimSpace(capacity.Availability))
	availabilityIndicator := CommandPolicyIndicator{
		Name:     "availability",
		Source:   capacitySource(capacity.Source),
		Value:    availability,
		Severity: "info",
		Message:  "Grafana availability signal",
	}
	if availability == "degraded" {
		availabilityIndicator.Severity = "critical"
		availabilityIndicator.Effect = CommandPolicyDecisionDeny
		availabilityIndicator.DroveOutcome = true
		availabilityIndicator.Message = "capacity degraded; command denied by policy"
		decision.Outcome = CommandPolicyDecisionDeny
	} else if availability == "limited" || availability == "insufficient" {
		availabilityIndicator.Severity = "warn"
		availabilityIndicator.Effect = CommandPolicyDecisionQueue
		availabilityIndicator.DroveOutcome = decision.Outcome != CommandPolicyDecisionDeny
		availabilityIndicator.Message = "capacity limited; command queued for operator decision"
		decision.Outcome = mergeDecisionOutcome(decision.Outcome, CommandPolicyDecisionQueue)
	}
	decision.Rationale.Indicators = append(decision.Rationale.Indicators, availabilityIndicator)

	datasourceThreshold := float64(thresholds.MinDatasourceCount)
	datasourceIndicator := CommandPolicyIndicator{
		Name:       "datasource_count",
		Source:     capacitySource(capacity.Source),
		Value:      capacity.DatasourceCount,
		Comparator: ">=",
		Threshold:  thresholds.MinDatasourceCount,
		Severity:   "info",
		Message:    "minimum datasource count check",
	}
	if float64(capacity.DatasourceCount) < datasourceThreshold {
		datasourceIndicator.Severity = "critical"
		datasourceIndicator.Effect = CommandPolicyDecisionDeny
		datasourceIndicator.DroveOutcome = true
		datasourceIndicator.Message = "insufficient datasource coverage; command denied"
		decision.Outcome = CommandPolicyDecisionDeny
	}
	decision.Rationale.Indicators = append(decision.Rationale.Indicators, datasourceIndicator)

	if capacity.DashboardCoverage > 0 {
		dashboardIndicator := CommandPolicyIndicator{
			Name:       "dashboard_coverage",
			Source:     capacitySource(capacity.Source),
			Value:      capacity.DashboardCoverage,
			Comparator: ">=",
			Threshold:  thresholds.MinDashboardCoverage,
			Severity:   "info",
			Message:    "dashboard coverage threshold",
		}
		if capacity.DashboardCoverage < thresholds.MinDashboardCoverage {
			dashboardIndicator.Severity = "warn"
			dashboardIndicator.Effect = CommandPolicyDecisionQueue
			dashboardIndicator.DroveOutcome = decision.Outcome == CommandPolicyDecisionAllow
			dashboardIndicator.Message = "dashboard coverage below threshold; command queued"
			decision.Outcome = mergeDecisionOutcome(decision.Outcome, CommandPolicyDecisionQueue)
		}
		decision.Rationale.Indicators = append(decision.Rationale.Indicators, dashboardIndicator)
	}

	if capacity.QueryCoverage > 0 {
		queryIndicator := CommandPolicyIndicator{
			Name:       "query_coverage",
			Source:     capacitySource(capacity.Source),
			Value:      capacity.QueryCoverage,
			Comparator: ">=",
			Threshold:  thresholds.MinQueryCoverage,
			Severity:   "info",
			Message:    "query coverage threshold",
		}
		if capacity.QueryCoverage < thresholds.MinQueryCoverage {
			queryIndicator.Severity = "warn"
			queryIndicator.Effect = CommandPolicyDecisionQueue
			queryIndicator.DroveOutcome = decision.Outcome == CommandPolicyDecisionAllow
			queryIndicator.Message = "query coverage below threshold; command queued"
			decision.Outcome = mergeDecisionOutcome(decision.Outcome, CommandPolicyDecisionQueue)
		}
		decision.Rationale.Indicators = append(decision.Rationale.Indicators, queryIndicator)
	}

	if capacity.Partial {
		decision.Rationale.Indicators = append(decision.Rationale.Indicators, CommandPolicyIndicator{
			Name:     "partial_snapshot",
			Source:   capacitySource(capacity.Source),
			Value:    true,
			Severity: "warn",
			Message:  "capacity snapshot is partial; non-deny safeguards remain active",
		})
	}

	decision.Rationale.Summary = summarizeDecision(decision.Outcome, decision.Rationale.Indicators)
	return decision
}

func (s *Service) readCapacitySignals(ctx context.Context) (*CapacitySignals, error) {
	if s == nil || s.capacitySignalSource == nil {
		return nil, nil
	}
	signals, err := s.capacitySignalSource.CapacitySignals(ctx)
	if err != nil {
		return nil, err
	}
	if signals == nil {
		return nil, nil
	}
	clone := *signals
	clone.Source = capacitySource(clone.Source)
	clone.Availability = strings.ToLower(strings.TrimSpace(clone.Availability))
	clone.Warnings = cloneStrings(clone.Warnings)
	return &clone, nil
}

func mergeDecisionOutcome(current, candidate CommandPolicyDecisionOutcome) CommandPolicyDecisionOutcome {
	rank := func(outcome CommandPolicyDecisionOutcome) int {
		switch outcome {
		case CommandPolicyDecisionAllow:
			return 1
		case CommandPolicyDecisionQueue:
			return 2
		case CommandPolicyDecisionDeny:
			return 3
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return candidate
	}
	return current
}

func summarizeDecision(outcome CommandPolicyDecisionOutcome, indicators []CommandPolicyIndicator) string {
	drivers := make([]string, 0, len(indicators))
	for _, indicator := range indicators {
		if !indicator.DroveOutcome {
			continue
		}
		msg := strings.TrimSpace(indicator.Message)
		if msg == "" {
			msg = indicator.Name
		}
		drivers = append(drivers, msg)
	}
	if len(drivers) == 0 {
		switch outcome {
		case CommandPolicyDecisionAllow:
			return "command allowed by policy"
		case CommandPolicyDecisionQueue:
			return "command queued for approval"
		case CommandPolicyDecisionDeny:
			return "command denied by policy"
		default:
			return "policy decision completed"
		}
	}
	return fmt.Sprintf("%s (%s)", outcome, strings.Join(drivers, "; "))
}

func capacitySource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "grafana"
	}
	return source
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
