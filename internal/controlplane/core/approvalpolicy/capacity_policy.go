package approvalpolicy

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	controlpolicy "github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/protocol"
)

// CommandPolicyDecisionOutcome captures the policy action for a command request.
type CommandPolicyDecisionOutcome string

const (
	CommandPolicyDecisionAllow CommandPolicyDecisionOutcome = "allow"
	CommandPolicyDecisionDeny  CommandPolicyDecisionOutcome = "deny"
	CommandPolicyDecisionQueue CommandPolicyDecisionOutcome = "queue"
)

// CommandPolicyGateOutcome is a stable API-visible gate decision.
type CommandPolicyGateOutcome string

const (
	CommandPolicyGateAllowed         CommandPolicyGateOutcome = "allowed"
	CommandPolicyGateBlocked         CommandPolicyGateOutcome = "blocked"
	CommandPolicyGatePendingApproval CommandPolicyGateOutcome = "pending_approval"
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

// CommandClassification captures deterministic command classification metadata.
type CommandClassification struct {
	Capability     protocol.CapabilityLevel `json:"capability"`
	Category       string                   `json:"category"`
	SignatureKnown bool                     `json:"signature_known"`
	ReasonCode     string                   `json:"reason_code"`
}

// CommandPolicyProfile captures the policy-v2 inputs used for lane resolution.
type CommandPolicyProfile struct {
	PolicyID               string                    `json:"policy_id,omitempty"`
	ExecutionClassRequired protocol.ExecutionClass   `json:"execution_class_required"`
	SandboxRequired        bool                      `json:"sandbox_required"`
	ApprovalMode           protocol.ApprovalMode     `json:"approval_mode"`
	RequireSecondApprover  bool                      `json:"require_second_approver,omitempty"`
	Breakglass             protocol.BreakglassPolicy `json:"breakglass"`
}

// CommandPolicyDecision is the normalized policy decision for a command.
type CommandPolicyDecision struct {
	Outcome        CommandPolicyDecisionOutcome `json:"outcome"`
	GateOutcome    CommandPolicyGateOutcome     `json:"gate_outcome"`
	Lane           protocol.ExecutionClass      `json:"lane"`
	RiskLevel      string                       `json:"risk_level"`
	RiskTier       int                          `json:"risk_tier"`
	ReasonCode     string                       `json:"reason_code"`
	Classification CommandClassification        `json:"classification"`
	Policy         CommandPolicyProfile         `json:"policy"`
	Rationale      CommandPolicyRationale       `json:"rationale"`
}

// CommandPolicyRationale is a machine-readable explanation for policy outcomes.
type CommandPolicyRationale struct {
	Policy     string                     `json:"policy"`
	Summary    string                     `json:"summary"`
	Fallback   bool                       `json:"fallback"`
	Indicators []CommandPolicyIndicator   `json:"indicators,omitempty"`
	Capacity   *CapacitySignals           `json:"capacity,omitempty"`
	Thresholds CapacityThresholdsSnapshot `json:"thresholds"`
	Lane       CommandPolicyLaneRationale `json:"lane"`
}

// CommandPolicyLaneRationale surfaces lane-selection and gate rationale.
type CommandPolicyLaneRationale struct {
	SelectedLane protocol.ExecutionClass  `json:"selected_lane"`
	GateOutcome  CommandPolicyGateOutcome `json:"gate_outcome"`
	ReasonCode   string                   `json:"reason_code"`
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
	return s.evaluateCommandPolicy(ctx, "", cmd, probeLevel, nil)
}

// EvaluateCommandPolicyForProbe evaluates command policy with probe-specific policy-v2 context when available.
func (s *Service) EvaluateCommandPolicyForProbe(ctx context.Context, probeID string, cmd *protocol.CommandPayload, probeLevel protocol.CapabilityLevel) CommandPolicyDecision {
	return s.evaluateCommandPolicy(ctx, probeID, cmd, probeLevel, nil)
}

// EvaluateCommandPolicyPreview evaluates command policy using an optional explicit policy override.
func (s *Service) EvaluateCommandPolicyPreview(ctx context.Context, probeID string, cmd *protocol.CommandPayload, probeLevel protocol.CapabilityLevel, override *CommandPolicyProfile) CommandPolicyDecision {
	return s.evaluateCommandPolicy(ctx, probeID, cmd, probeLevel, override)
}

func (s *Service) evaluateCommandPolicy(ctx context.Context, probeID string, cmd *protocol.CommandPayload, probeLevel protocol.CapabilityLevel, override *CommandPolicyProfile) CommandPolicyDecision {
	thresholds := s.capacityThresholds.normalized()
	if cmd == nil {
		cmd = &protocol.CommandPayload{}
	}

	classification := classifyCommandWithMetadata(cmd.Command, cmd.Args)
	risk := approval.ClassifyRisk(cmd)
	riskTier := riskTierForLevel(risk)

	policyProfile := s.resolvePolicyProfile(probeID, probeLevel, override)
	laneResolution := resolveLaneDecision(classification, policyProfile)

	decision := CommandPolicyDecision{
		Outcome:        decisionOutcomeFromGate(laneResolution.GateOutcome),
		GateOutcome:    laneResolution.GateOutcome,
		Lane:           laneResolution.SelectedLane,
		RiskLevel:      risk,
		RiskTier:       riskTier,
		ReasonCode:     laneResolution.ReasonCode,
		Classification: toClassification(classification),
		Policy:         policyProfile,
		Rationale: CommandPolicyRationale{
			Policy:   capacityPolicyVersion,
			Fallback: false,
			Thresholds: CapacityThresholdsSnapshot{
				MinDatasourceCount:   thresholds.MinDatasourceCount,
				MinDashboardCoverage: thresholds.MinDashboardCoverage,
				MinQueryCoverage:     thresholds.MinQueryCoverage,
			},
			Lane: CommandPolicyLaneRationale{
				SelectedLane: laneResolution.SelectedLane,
				GateOutcome:  laneResolution.GateOutcome,
				ReasonCode:   laneResolution.ReasonCode,
			},
		},
	}
	if decision.ReasonCode == "" {
		decision.ReasonCode = "policy.allowed"
	}

	riskIndicator := CommandPolicyIndicator{
		Name:     "command_risk",
		Source:   "risk_classifier",
		Value:    risk,
		Severity: "info",
		Message:  "command risk classification",
	}
	if (risk == "high" || risk == "critical") && decision.Outcome == CommandPolicyDecisionAllow {
		decision.Outcome = CommandPolicyDecisionQueue
		decision.GateOutcome = decisionGateFromOutcome(decision.Outcome)
		decision.ReasonCode = "approval.required.risk_high"
		riskIndicator.Effect = CommandPolicyDecisionQueue
		riskIndicator.DroveOutcome = true
		riskIndicator.Severity = "warn"
		riskIndicator.Message = "high-risk command requires human approval"
	}
	decision.Rationale.Indicators = append(decision.Rationale.Indicators, riskIndicator)

	// Deny-by-default policy blocks are fail-closed and do not depend on external capacity signals.
	if decision.Outcome == CommandPolicyDecisionDeny && strings.HasPrefix(decision.ReasonCode, "policy.") {
		decision.Rationale.Summary = summarizeDecision(decision.Outcome, decision.Rationale.Indicators)
		return decision
	}

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
		availabilityIndicator.DroveOutcome = decision.Outcome != CommandPolicyDecisionDeny
		availabilityIndicator.Message = "capacity degraded; command denied by policy"
		decision = mergeDecisionWithReason(decision, CommandPolicyDecisionDeny, "capacity.availability_degraded")
	} else if availability == "limited" || availability == "insufficient" {
		availabilityIndicator.Severity = "warn"
		availabilityIndicator.Effect = CommandPolicyDecisionQueue
		availabilityIndicator.DroveOutcome = decision.Outcome == CommandPolicyDecisionAllow
		availabilityIndicator.Message = "capacity limited; command queued for operator decision"
		decision = mergeDecisionWithReason(decision, CommandPolicyDecisionQueue, "capacity.availability_limited")
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
		datasourceIndicator.DroveOutcome = decision.Outcome != CommandPolicyDecisionDeny
		datasourceIndicator.Message = "insufficient datasource coverage; command denied"
		decision = mergeDecisionWithReason(decision, CommandPolicyDecisionDeny, "capacity.datasource_insufficient")
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
			decision = mergeDecisionWithReason(decision, CommandPolicyDecisionQueue, "capacity.dashboard_coverage_low")
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
			decision = mergeDecisionWithReason(decision, CommandPolicyDecisionQueue, "capacity.query_coverage_low")
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

func (s *Service) resolvePolicyProfile(probeID string, probeLevel protocol.CapabilityLevel, override *CommandPolicyProfile) CommandPolicyProfile {
	defaults := controlpolicy.DefaultTemplateOptionsForLevel(probeLevel)
	profile := CommandPolicyProfile{
		ExecutionClassRequired: defaults.ExecutionClassRequired,
		SandboxRequired:        defaults.SandboxRequired,
		ApprovalMode:           defaults.ApprovalMode,
		RequireSecondApprover:  defaults.RequireSecondApprover,
	}

	if ctx, ok := s.appliedPolicyForProbe(probeID); ok {
		profile.PolicyID = strings.TrimSpace(ctx.PolicyID)
		profile.ExecutionClassRequired = protocol.ExecutionClass(strings.TrimSpace(string(ctx.Options.ExecutionClassRequired)))
		profile.SandboxRequired = ctx.Options.SandboxRequired
		profile.ApprovalMode = protocol.ApprovalMode(strings.TrimSpace(string(ctx.Options.ApprovalMode)))
		profile.RequireSecondApprover = ctx.Options.RequireSecondApprover
		profile.Breakglass = ctx.Options.Breakglass
	}

	if override != nil {
		if strings.TrimSpace(override.PolicyID) != "" {
			profile.PolicyID = strings.TrimSpace(override.PolicyID)
		}
		if strings.TrimSpace(string(override.ExecutionClassRequired)) != "" {
			profile.ExecutionClassRequired = protocol.ExecutionClass(strings.TrimSpace(string(override.ExecutionClassRequired)))
		}
		profile.SandboxRequired = override.SandboxRequired
		if strings.TrimSpace(string(override.ApprovalMode)) != "" {
			profile.ApprovalMode = protocol.ApprovalMode(strings.TrimSpace(string(override.ApprovalMode)))
		}
		profile.RequireSecondApprover = override.RequireSecondApprover
		profile.Breakglass = override.Breakglass
	}

	if profile.ExecutionClassRequired == "" {
		profile.ExecutionClassRequired = defaults.ExecutionClassRequired
	}
	if profile.ApprovalMode == "" {
		profile.ApprovalMode = defaults.ApprovalMode
	}
	return profile
}

type commandClassifierResult struct {
	Level          protocol.CapabilityLevel
	Category       string
	SignatureKnown bool
	ReasonCode     string
}

func classifyCommandWithMetadata(command string, args []string) commandClassifierResult {
	fullLower, baseLower := normalizeCommandParts(command, args)

	remediatePrefixes := []string{
		"rm ", "rm\t", "mv ", "mv\t", "cp ", "cp\t",
		"chmod ", "chown ", "chgrp ",
		"mkdir ", "touch ", "tee ",
		"apt ", "apt-get ", "dpkg ", "yum ", "dnf ", "rpm ",
		"pip ", "pip3 ", "npm ", "gem ",
		"systemctl start", "systemctl stop", "systemctl restart",
		"systemctl enable", "systemctl disable", "systemctl mask",
		"service ", "reboot", "shutdown", "poweroff", "halt", "init ",
		"kill ", "pkill ", "killall ",
		"iptables ", "ip6tables ", "ufw ", "firewall-cmd ",
		"sed -i", "dd ", "mkfs", "mount ",
		"useradd", "userdel", "usermod", "groupadd", "groupdel",
		"passwd ", "chpasswd", "crontab ", "kubeflow cancel",
	}
	for _, prefix := range remediatePrefixes {
		if strings.HasPrefix(fullLower, prefix) || strings.HasPrefix(baseLower, prefix) {
			return commandClassifierResult{Level: protocol.CapRemediate, Category: "mutation", SignatureKnown: true, ReasonCode: "classifier.remediate_prefix"}
		}
	}

	observePrefixes := []string{
		"ip addr", "ip route", "ip link", "ip neigh",
		"systemctl status", "systemctl is-active", "systemctl is-enabled",
		"systemctl list-units", "systemctl list-timers",
		"docker ps", "docker images", "docker inspect",
		"podman ps", "podman images", "podman inspect",
	}
	for _, prefix := range observePrefixes {
		if strings.HasPrefix(fullLower, prefix) {
			return commandClassifierResult{Level: protocol.CapObserve, Category: "observe", SignatureKnown: true, ReasonCode: "classifier.observe_prefix"}
		}
	}

	observeCommands := map[string]struct{}{
		"cat": {}, "ls": {}, "head": {}, "tail": {}, "df": {}, "du": {}, "free": {}, "uptime": {}, "uname": {},
		"hostname": {}, "whoami": {}, "id": {}, "ps": {}, "top": {}, "netstat": {}, "ss": {},
		"lsof": {}, "file": {}, "stat": {}, "wc": {}, "grep": {}, "find": {}, "journalctl": {},
		"which": {}, "type": {}, "echo": {}, "date": {}, "env": {}, "printenv": {}, "lsb_release": {},
		"arch": {}, "nproc": {}, "getent": {}, "groups": {}, "last": {}, "w": {}, "sleep": {}, "true": {}, "false": {},
		"sh": {}, "bash": {}, // defence-in-depth: shell invocation is observe-level; inner command determines real risk
	}
	if _, ok := observeCommands[baseLower]; ok {
		if baseLower == "find" && (strings.Contains(fullLower, "-exec") || strings.Contains(fullLower, "-delete")) {
			return commandClassifierResult{Level: protocol.CapRemediate, Category: "mutation", SignatureKnown: true, ReasonCode: "classifier.find_mutation_flags"}
		}
		return commandClassifierResult{Level: protocol.CapObserve, Category: "observe", SignatureKnown: true, ReasonCode: "classifier.observe_command"}
	}

	diagnosePrefixes := []string{"curl", "wget", "fdisk -l", "mount", "kubeflow submit"}
	for _, prefix := range diagnosePrefixes {
		if strings.HasPrefix(fullLower, prefix) {
			if strings.HasPrefix(baseLower, "wget") && strings.Contains(fullLower, " -o") {
				return commandClassifierResult{Level: protocol.CapRemediate, Category: "mutation", SignatureKnown: true, ReasonCode: "classifier.wget_output_file"}
			}
			return commandClassifierResult{Level: protocol.CapDiagnose, Category: "diagnose", SignatureKnown: true, ReasonCode: "classifier.diagnose_prefix"}
		}
	}

	diagnoseCommands := map[string]struct{}{
		"strace": {}, "ltrace": {}, "tcpdump": {}, "dig": {}, "nslookup": {}, "traceroute": {},
		"tracepath": {}, "ping": {}, "openssl": {}, "nc": {}, "ncat": {}, "nmap": {},
		"iotop": {}, "vmstat": {}, "iostat": {}, "sar": {}, "dmesg": {}, "lsblk": {},
		"blkid": {}, "ss": {}, "perf": {}, "iftop": {}, "nethogs": {},
	}
	if _, ok := diagnoseCommands[baseLower]; ok {
		return commandClassifierResult{Level: protocol.CapDiagnose, Category: "diagnose", SignatureKnown: true, ReasonCode: "classifier.diagnose_command"}
	}

	return commandClassifierResult{Level: protocol.CapRemediate, Category: "mutation", SignatureKnown: false, ReasonCode: "classifier.unknown_mutation_signature"}
}

func normalizeCommandParts(command string, args []string) (string, string) {
	trimmed := strings.TrimSpace(command)
	full := trimmed
	if len(args) > 0 {
		full = trimmed + " " + strings.Join(args, " ")
	}
	fullLower := strings.ToLower(strings.TrimSpace(full))
	base := trimmed
	if idx := strings.IndexAny(base, " \t"); idx > 0 {
		base = base[:idx]
	}
	baseLower := strings.ToLower(strings.TrimSpace(base))
	return fullLower, baseLower
}

func toClassification(classification commandClassifierResult) CommandClassification {
	return CommandClassification{
		Capability:     classification.Level,
		Category:       classification.Category,
		SignatureKnown: classification.SignatureKnown,
		ReasonCode:     classification.ReasonCode,
	}
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

type laneResolutionResult struct {
	SelectedLane protocol.ExecutionClass
	GateOutcome  CommandPolicyGateOutcome
	ReasonCode   string
}

func resolveLaneDecision(classification commandClassifierResult, profile CommandPolicyProfile) laneResolutionResult {
	selectedLane := laneFromCapability(classification.Level)
	reasonCode := "policy.allowed"

	if profile.ExecutionClassRequired == protocol.ExecBreakglassDirect {
		selectedLane = protocol.ExecBreakglassDirect
	} else if profile.ExecutionClassRequired != "" {
		selectedLane = stricterLane(selectedLane, profile.ExecutionClassRequired)
	}

	result := laneResolutionResult{
		SelectedLane: selectedLane,
		GateOutcome:  CommandPolicyGateAllowed,
		ReasonCode:   reasonCode,
	}

	if classification.Level == protocol.CapRemediate {
		if !classification.SignatureKnown {
			result.GateOutcome = CommandPolicyGateBlocked
			result.ReasonCode = "policy.lane_unmapped"
			if result.SelectedLane == "" {
				result.SelectedLane = protocol.ExecRemediateSandbox
			}
			return result
		}
		if result.SelectedLane == protocol.ExecBreakglassDirect {
			if !profile.Breakglass.Enabled {
				result.GateOutcome = CommandPolicyGateBlocked
				result.ReasonCode = "policy.breakglass_disabled"
				return result
			}
			if profile.SandboxRequired {
				result.GateOutcome = CommandPolicyGateBlocked
				result.ReasonCode = "policy.sandbox_required"
				return result
			}
			result.GateOutcome = CommandPolicyGatePendingApproval
			result.ReasonCode = "approval.breakglass_required"
			return result
		}
		if result.SelectedLane == protocol.ExecObserveDirect {
			result.GateOutcome = CommandPolicyGateBlocked
			result.ReasonCode = "policy.lane_unmapped"
			return result
		}
		if result.SelectedLane == protocol.ExecDiagnoseSandbox {
			result.SelectedLane = protocol.ExecRemediateSandbox
			result.ReasonCode = "policy.lane_escalated"
		}
	}

	if profile.SandboxRequired {
		switch result.SelectedLane {
		case protocol.ExecObserveDirect:
			result.SelectedLane = protocol.ExecDiagnoseSandbox
			if result.ReasonCode == "policy.allowed" {
				result.ReasonCode = "policy.sandbox_escalated"
			}
		case protocol.ExecBreakglassDirect:
			result.GateOutcome = CommandPolicyGateBlocked
			result.ReasonCode = "policy.sandbox_required"
			return result
		}
	}

	if classification.Level != protocol.CapRemediate && result.SelectedLane == protocol.ExecBreakglassDirect {
		result.GateOutcome = CommandPolicyGateBlocked
		result.ReasonCode = "policy.breakglass_not_applicable"
		return result
	}

	if result.GateOutcome == CommandPolicyGateBlocked {
		return result
	}

	switch profile.ApprovalMode {
	case protocol.ApprovalNone:
		// no gate
	case protocol.ApprovalMutationGate:
		if classification.Level == protocol.CapRemediate || result.SelectedLane == protocol.ExecBreakglassDirect {
			result.GateOutcome = CommandPolicyGatePendingApproval
			result.ReasonCode = "approval.required.mutation_gate"
		}
	case protocol.ApprovalPlanFirst:
		result.GateOutcome = CommandPolicyGatePendingApproval
		result.ReasonCode = "approval.required.plan_first"
	case protocol.ApprovalEveryAction:
		result.GateOutcome = CommandPolicyGatePendingApproval
		result.ReasonCode = "approval.required.every_action"
	case protocol.ApprovalTwoPerson:
		result.GateOutcome = CommandPolicyGatePendingApproval
		result.ReasonCode = "approval.required.two_person"
	default:
		if classification.Level == protocol.CapRemediate {
			result.GateOutcome = CommandPolicyGatePendingApproval
			result.ReasonCode = "approval.required.mutation_gate"
		}
	}

	if result.ReasonCode == "" {
		result.ReasonCode = "policy.allowed"
	}
	return result
}

func laneFromCapability(level protocol.CapabilityLevel) protocol.ExecutionClass {
	switch level {
	case protocol.CapDiagnose:
		return protocol.ExecDiagnoseSandbox
	case protocol.CapRemediate:
		return protocol.ExecRemediateSandbox
	case protocol.CapObserve:
		fallthrough
	default:
		return protocol.ExecObserveDirect
	}
}

func stricterLane(current, candidate protocol.ExecutionClass) protocol.ExecutionClass {
	if current == "" {
		return candidate
	}
	if candidate == "" {
		return current
	}
	if candidate == protocol.ExecBreakglassDirect {
		return candidate
	}

	rank := func(lane protocol.ExecutionClass) int {
		switch lane {
		case protocol.ExecObserveDirect:
			return 1
		case protocol.ExecDiagnoseSandbox:
			return 2
		case protocol.ExecRemediateSandbox:
			return 3
		case protocol.ExecBreakglassDirect:
			return 4
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return candidate
	}
	return current
}

func mergeDecisionWithReason(decision CommandPolicyDecision, candidate CommandPolicyDecisionOutcome, reasonCode string) CommandPolicyDecision {
	merged := mergeDecisionOutcome(decision.Outcome, candidate)
	if merged != decision.Outcome {
		decision.Outcome = merged
		decision.GateOutcome = decisionGateFromOutcome(merged)
		if strings.TrimSpace(reasonCode) != "" {
			decision.ReasonCode = reasonCode
		}
	}
	if decision.ReasonCode == "" {
		decision.ReasonCode = "policy.allowed"
	}
	return decision
}

func decisionOutcomeFromGate(outcome CommandPolicyGateOutcome) CommandPolicyDecisionOutcome {
	switch outcome {
	case CommandPolicyGateBlocked:
		return CommandPolicyDecisionDeny
	case CommandPolicyGatePendingApproval:
		return CommandPolicyDecisionQueue
	case CommandPolicyGateAllowed:
		fallthrough
	default:
		return CommandPolicyDecisionAllow
	}
}

func decisionGateFromOutcome(outcome CommandPolicyDecisionOutcome) CommandPolicyGateOutcome {
	switch outcome {
	case CommandPolicyDecisionDeny:
		return CommandPolicyGateBlocked
	case CommandPolicyDecisionQueue:
		return CommandPolicyGatePendingApproval
	case CommandPolicyDecisionAllow:
		fallthrough
	default:
		return CommandPolicyGateAllowed
	}
}

func riskTierForLevel(risk string) int {
	switch strings.TrimSpace(strings.ToLower(risk)) {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	default:
		return 2
	}
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
