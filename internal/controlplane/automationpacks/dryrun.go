package automationpacks

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/protocol"
)

const (
	PolicyOutcomeAllow = "allow"
	PolicyOutcomeQueue = "queue"
	PolicyOutcomeDeny  = "deny"
)

var (
	exactInputTemplatePattern  = regexp.MustCompile(`^\{\{\s*inputs\.([A-Za-z0-9._-]+)\s*\}\}$`)
	inlineInputTemplatePattern = regexp.MustCompile(`\{\{\s*inputs\.([A-Za-z0-9._-]+)\s*\}\}`)
)

// DryRunRequest runs planning/simulation against a definition+inputs payload.
type DryRunRequest struct {
	Definition Definition     `json:"definition"`
	Inputs     map[string]any `json:"inputs,omitempty"`
}

// DryRunResult captures non-mutating execution planning + policy predictions.
type DryRunResult struct {
	NonMutating      bool               `json:"non_mutating"`
	Metadata         Metadata           `json:"metadata"`
	ResolvedInputs   map[string]any     `json:"resolved_inputs,omitempty"`
	Steps            []DryRunStepResult `json:"steps"`
	ExpectedOutcomes []ExpectedOutcome  `json:"expected_outcomes,omitempty"`
	WorkflowPolicy   PolicySimulation   `json:"workflow_policy_simulation"`
	RiskSummary      DryRunRiskSummary  `json:"risk_summary"`
}

// DryRunStepResult is one step in the resolved execution plan.
type DryRunStepResult struct {
	Order             int               `json:"order"`
	ID                string            `json:"id"`
	Name              string            `json:"name,omitempty"`
	Description       string            `json:"description,omitempty"`
	Action            string            `json:"action"`
	ResolvedParams    map[string]any    `json:"resolved_parameters,omitempty"`
	PredictedRisk     string            `json:"predicted_risk"`
	ApprovalRequired  bool              `json:"approval_required"`
	PolicySimulation  PolicySimulation  `json:"policy_simulation"`
	PredictedOutcomes []ExpectedOutcome `json:"predicted_outcomes,omitempty"`
}

// DryRunRiskSummary is a compact policy prediction roll-up.
type DryRunRiskSummary struct {
	AllowCount int      `json:"allow_count"`
	QueueCount int      `json:"queue_count"`
	DenyCount  int      `json:"deny_count"`
	Highest    string   `json:"highest"`
	Reasons    []string `json:"reasons,omitempty"`
}

// PolicySimulationRequest captures context for one policy simulation call.
type PolicySimulationRequest struct {
	Definition Metadata                `json:"definition"`
	Step       Step                    `json:"step"`
	Command    protocol.CommandPayload `json:"command"`
}

// PolicySimulation is the normalized allow/queue/deny prediction for dry-run.
type PolicySimulation struct {
	Outcome   string `json:"outcome"`
	RiskLevel string `json:"risk_level,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Rationale any    `json:"rationale,omitempty"`
}

// PolicySimulator evaluates step commands using existing policy logic in simulation mode.
type PolicySimulator interface {
	Simulate(ctx context.Context, req PolicySimulationRequest) PolicySimulation
}

// PolicySimulatorFunc adapts function callbacks to PolicySimulator.
type PolicySimulatorFunc func(ctx context.Context, req PolicySimulationRequest) PolicySimulation

func (fn PolicySimulatorFunc) Simulate(ctx context.Context, req PolicySimulationRequest) PolicySimulation {
	if fn == nil {
		return noopPolicySimulator{}.Simulate(ctx, req)
	}
	return fn(ctx, req)
}

type noopPolicySimulator struct{}

func (noopPolicySimulator) Simulate(_ context.Context, req PolicySimulationRequest) PolicySimulation {
	risk := approval.ClassifyRisk(&req.Command)
	return PolicySimulation{
		Outcome:   PolicyOutcomeAllow,
		RiskLevel: risk,
		Summary:   "policy simulator unavailable; default allow",
	}
}

// InputValidationError captures user input validation failures for dry-run.
type InputValidationError struct {
	Issues []string `json:"issues"`
}

func (e *InputValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "input validation failed"
	}
	return "input validation failed: " + strings.Join(e.Issues, "; ")
}

func runDryRun(ctx context.Context, req DryRunRequest, simulator PolicySimulator) (*DryRunResult, error) {
	def := req.Definition
	if err := ValidateDefinition(&def); err != nil {
		return nil, err
	}

	resolvedInputs, err := resolveInputs(def.Inputs, req.Inputs)
	if err != nil {
		return nil, err
	}

	if simulator == nil {
		simulator = noopPolicySimulator{}
	}

	steps := make([]DryRunStepResult, 0, len(def.Steps))
	summary := DryRunRiskSummary{Highest: PolicyOutcomeAllow}
	workflowReasons := make([]string, 0)
	workflowOutcome := PolicyOutcomeAllow

	for idx, step := range def.Steps {
		resolvedParams := resolveStepParameters(step.Parameters, resolvedInputs)
		command := commandPayloadForStep(step, resolvedParams, idx)
		risk := approval.ClassifyRisk(&command)
		prediction := simulator.Simulate(ctx, PolicySimulationRequest{
			Definition: def.Metadata,
			Step:       step,
			Command:    command,
		})
		if prediction.Outcome == "" {
			prediction.Outcome = PolicyOutcomeAllow
		}
		if prediction.RiskLevel == "" {
			prediction.RiskLevel = risk
		}
		prediction = applyApprovalSimulation(prediction, step.Approval, fmt.Sprintf("step %q", step.ID))

		steps = append(steps, DryRunStepResult{
			Order:             idx + 1,
			ID:                step.ID,
			Name:              step.Name,
			Description:       step.Description,
			Action:            step.Action,
			ResolvedParams:    resolvedParams,
			PredictedRisk:     risk,
			ApprovalRequired:  step.Approval != nil && step.Approval.Required,
			PolicySimulation:  prediction,
			PredictedOutcomes: cloneOutcomes(step.ExpectedOutcomes),
		})

		workflowOutcome = mergePolicyOutcome(workflowOutcome, prediction.Outcome)
		updateRiskSummary(&summary, prediction)
		if text := strings.TrimSpace(prediction.Summary); text != "" && prediction.Outcome != PolicyOutcomeAllow {
			workflowReasons = append(workflowReasons, fmt.Sprintf("step %s: %s", step.ID, text))
		}
	}

	workflowPrediction := PolicySimulation{
		Outcome: workflowOutcome,
	}
	workflowPrediction = applyApprovalSimulation(workflowPrediction, def.Approval, "workflow")
	if workflowPrediction.Outcome != workflowOutcome {
		updateRiskSummary(&summary, workflowPrediction)
	}
	workflowOutcome = workflowPrediction.Outcome
	summary.Highest = workflowOutcome
	if workflowPrediction.Summary != "" {
		workflowReasons = append(workflowReasons, workflowPrediction.Summary)
	}
	if len(workflowReasons) == 0 {
		workflowPrediction.Summary = "all steps predicted allow"
	} else {
		workflowPrediction.Summary = strings.Join(uniqueStrings(workflowReasons), "; ")
		summary.Reasons = uniqueStrings(workflowReasons)
	}

	result := &DryRunResult{
		NonMutating:      true,
		Metadata:         def.Metadata,
		ResolvedInputs:   resolvedInputs,
		Steps:            steps,
		ExpectedOutcomes: cloneOutcomes(def.ExpectedOutcomes),
		WorkflowPolicy:   workflowPrediction,
		RiskSummary:      summary,
	}
	return result, nil
}

func resolveInputs(defInputs []Input, provided map[string]any) (map[string]any, error) {
	provided = cloneMap(provided)
	resolved := make(map[string]any, len(defInputs))
	issues := make([]string, 0)
	known := make(map[string]struct{}, len(defInputs))

	for _, input := range defInputs {
		known[input.Name] = struct{}{}
		value, ok := provided[input.Name]
		if !ok {
			if input.Default != nil {
				value = cloneValue(input.Default)
			} else if input.Required {
				issues = append(issues, fmt.Sprintf("inputs.%s is required", input.Name))
				continue
			} else {
				continue
			}
		}

		if !valueMatchesType(value, input.Type) {
			issues = append(issues, fmt.Sprintf("inputs.%s must be type %s", input.Name, input.Type))
			continue
		}

		issues = append(issues, validateInputValue(input, value)...)
		resolved[input.Name] = cloneValue(value)
	}

	for name := range provided {
		if _, ok := known[name]; !ok {
			issues = append(issues, fmt.Sprintf("inputs.%s is not declared in definition", name))
		}
	}

	if len(issues) > 0 {
		return nil, &InputValidationError{Issues: uniqueStrings(issues)}
	}
	return resolved, nil
}

func validateInputValue(in Input, value any) []string {
	issues := make([]string, 0)
	prefix := fmt.Sprintf("inputs.%s", in.Name)
	c := in.Constraints

	switch in.Type {
	case InputTypeString:
		str, _ := value.(string)
		length := len(str)
		if c.MinLength != nil && length < *c.MinLength {
			issues = append(issues, fmt.Sprintf("%s must be at least %d characters", prefix, *c.MinLength))
		}
		if c.MaxLength != nil && length > *c.MaxLength {
			issues = append(issues, fmt.Sprintf("%s must be at most %d characters", prefix, *c.MaxLength))
		}
		if c.Pattern != "" {
			re, err := regexp.Compile(c.Pattern)
			if err == nil && !re.MatchString(str) {
				issues = append(issues, fmt.Sprintf("%s must match pattern %q", prefix, c.Pattern))
			}
		}

	case InputTypeNumber, InputTypeInteger:
		n, ok := toFloat64(value)
		if !ok {
			issues = append(issues, fmt.Sprintf("%s must be numeric", prefix))
			break
		}
		if c.Minimum != nil && n < *c.Minimum {
			issues = append(issues, fmt.Sprintf("%s must be >= %s", prefix, trimFloat(*c.Minimum)))
		}
		if c.Maximum != nil && n > *c.Maximum {
			issues = append(issues, fmt.Sprintf("%s must be <= %s", prefix, trimFloat(*c.Maximum)))
		}
		if in.Type == InputTypeInteger && !isInteger(value) {
			issues = append(issues, fmt.Sprintf("%s must be an integer", prefix))
		}

	case InputTypeArray:
		arr, _ := value.([]any)
		if c.MinItems != nil && len(arr) < *c.MinItems {
			issues = append(issues, fmt.Sprintf("%s must have at least %d items", prefix, *c.MinItems))
		}
		if c.MaxItems != nil && len(arr) > *c.MaxItems {
			issues = append(issues, fmt.Sprintf("%s must have at most %d items", prefix, *c.MaxItems))
		}
	}

	if len(c.Enum) > 0 {
		matched := false
		for _, candidate := range c.Enum {
			if valuesEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			issues = append(issues, fmt.Sprintf("%s must be one of the declared enum values", prefix))
		}
	}

	return issues
}

func commandPayloadForStep(step Step, resolved map[string]any, idx int) protocol.CommandPayload {
	command := strings.TrimSpace(step.Action)
	args := make([]string, 0)

	if v, ok := resolved["command"]; ok {
		if text, ok := v.(string); ok && strings.TrimSpace(text) != "" {
			command = strings.TrimSpace(text)
		}
	}
	if v, ok := resolved["args"]; ok {
		args = append(args, stringifyArgs(v)...)
	}
	if command == "" {
		command = "noop"
	}

	return protocol.CommandPayload{
		RequestID: fmt.Sprintf("dryrun-%s-%d", step.ID, idx+1),
		Command:   command,
		Args:      args,
		Level:     protocol.CapObserve,
	}
}

func stringifyArgs(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		if value == nil {
			return nil
		}
		return []string{fmt.Sprint(value)}
	}
}

func resolveStepParameters(params map[string]any, inputs map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	resolved := resolveValue(params, inputs)
	m, ok := resolved.(map[string]any)
	if !ok {
		return cloneMap(params)
	}
	return m
}

func resolveValue(value any, inputs map[string]any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = resolveValue(item, inputs)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for idx := range v {
			out[idx] = resolveValue(v[idx], inputs)
		}
		return out
	case string:
		return resolveStringValue(v, inputs)
	default:
		return cloneValue(v)
	}
}

func resolveStringValue(value string, inputs map[string]any) any {
	match := exactInputTemplatePattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) == 2 {
		if resolved, ok := inputs[match[1]]; ok {
			return cloneValue(resolved)
		}
		return value
	}

	resolved := inlineInputTemplatePattern.ReplaceAllStringFunc(value, func(token string) string {
		m := inlineInputTemplatePattern.FindStringSubmatch(token)
		if len(m) != 2 {
			return token
		}
		if v, ok := inputs[m[1]]; ok {
			return fmt.Sprint(v)
		}
		return token
	})
	return resolved
}

func applyApprovalSimulation(prediction PolicySimulation, approvalReq *ApprovalRequirement, scope string) PolicySimulation {
	if approvalReq == nil || !approvalReq.Required {
		return prediction
	}
	if strings.TrimSpace(prediction.Outcome) == "" {
		prediction.Outcome = PolicyOutcomeAllow
	}
	prediction.Outcome = mergePolicyOutcome(prediction.Outcome, PolicyOutcomeQueue)

	minApprovers := approvalReq.MinimumApprovers
	if minApprovers <= 0 {
		minApprovers = 1
	}
	note := fmt.Sprintf("%s requires manual approval (%d approver minimum)", scope, minApprovers)
	if prediction.Summary == "" {
		prediction.Summary = note
	} else if !strings.Contains(prediction.Summary, note) {
		prediction.Summary += "; " + note
	}
	return prediction
}

func mergePolicyOutcome(current, candidate string) string {
	rank := func(value string) int {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case PolicyOutcomeAllow:
			return 1
		case PolicyOutcomeQueue:
			return 2
		case PolicyOutcomeDeny:
			return 3
		default:
			return 0
		}
	}
	if rank(candidate) > rank(current) {
		return strings.ToLower(strings.TrimSpace(candidate))
	}
	if current == "" {
		return PolicyOutcomeAllow
	}
	return strings.ToLower(strings.TrimSpace(current))
}

func updateRiskSummary(summary *DryRunRiskSummary, prediction PolicySimulation) {
	if summary == nil {
		return
	}
	outcome := strings.ToLower(strings.TrimSpace(prediction.Outcome))
	if outcome == "" {
		outcome = PolicyOutcomeAllow
	}
	summary.Highest = mergePolicyOutcome(summary.Highest, outcome)
	switch outcome {
	case PolicyOutcomeAllow:
		summary.AllowCount++
	case PolicyOutcomeQueue:
		summary.QueueCount++
	case PolicyOutcomeDeny:
		summary.DenyCount++
	}
	if text := strings.TrimSpace(prediction.Summary); text != "" && outcome != PolicyOutcomeAllow {
		summary.Reasons = append(summary.Reasons, text)
	}
	summary.Reasons = uniqueStrings(summary.Reasons)
}

func cloneOutcomes(values []ExpectedOutcome) []ExpectedOutcome {
	if len(values) == 0 {
		return nil
	}
	out := make([]ExpectedOutcome, len(values))
	copy(out, values)
	return out
}

func cloneMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneValue(item)
	}
	return out
}

func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		out := make([]any, len(v))
		for idx := range v {
			out[idx] = cloneValue(v[idx])
		}
		return out
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	default:
		return v
	}
}

func valuesEqual(a, b any) bool {
	if fa, ok := toFloat64(a); ok {
		if fb, ok := toFloat64(b); ok {
			return fa == fb
		}
	}
	return reflect.DeepEqual(a, b)
}

func toFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}

func trimFloat(v float64) string {
	if math.Mod(v, 1) == 0 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
