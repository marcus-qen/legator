package automationpacks

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

var (
	definitionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,127}$`)
	semverPattern       = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
)

// ValidationError aggregates schema validation issues.
type ValidationError struct {
	Issues []string `json:"issues"`
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "validation failed"
	}
	return "validation failed: " + strings.Join(e.Issues, "; ")
}

// ValidateDefinition validates and normalizes an automation pack definition.
func ValidateDefinition(def *Definition) error {
	if def == nil {
		return &ValidationError{Issues: []string{"definition is required"}}
	}

	normalizeDefinition(def)

	issues := make([]string, 0)

	if def.Metadata.ID == "" {
		issues = append(issues, "metadata.id is required")
	} else if !definitionIDPattern.MatchString(def.Metadata.ID) {
		issues = append(issues, "metadata.id must match ^[a-z0-9][a-z0-9._-]{1,127}$")
	}

	if def.Metadata.Name == "" {
		issues = append(issues, "metadata.name is required")
	}

	if def.Metadata.Version == "" {
		issues = append(issues, "metadata.version is required")
	} else if !semverPattern.MatchString(def.Metadata.Version) {
		issues = append(issues, "metadata.version must be semantic version format (e.g. 1.0.0)")
	}

	if err := validateApproval("approval", def.Approval); err != "" {
		issues = append(issues, err)
	}

	inputNames := make(map[string]struct{}, len(def.Inputs))
	for idx := range def.Inputs {
		in := &def.Inputs[idx]
		prefix := fmt.Sprintf("inputs[%d]", idx)

		if in.Name == "" {
			issues = append(issues, prefix+".name is required")
		} else {
			if _, exists := inputNames[in.Name]; exists {
				issues = append(issues, fmt.Sprintf("%s.name %q must be unique", prefix, in.Name))
			} else {
				inputNames[in.Name] = struct{}{}
			}
		}

		if !isSupportedInputType(in.Type) {
			issues = append(issues, prefix+".type must be one of: string, number, integer, boolean, array, object")
		}

		issues = append(issues, validateInputConstraints(prefix, *in)...)
	}

	if len(def.Steps) == 0 {
		issues = append(issues, "steps must contain at least one step")
	}

	stepIDs := make(map[string]struct{}, len(def.Steps))
	totalOutcomes := len(def.ExpectedOutcomes)

	for idx := range def.Steps {
		step := &def.Steps[idx]
		prefix := fmt.Sprintf("steps[%d]", idx)

		if step.ID == "" {
			issues = append(issues, prefix+".id is required")
		} else {
			if _, exists := stepIDs[step.ID]; exists {
				issues = append(issues, fmt.Sprintf("%s.id %q must be unique", prefix, step.ID))
			} else {
				stepIDs[step.ID] = struct{}{}
			}
		}

		if step.Action == "" {
			issues = append(issues, prefix+".action is required")
		}

		if err := validateApproval(prefix+".approval", step.Approval); err != "" {
			issues = append(issues, err)
		}
		if step.TimeoutSeconds < 0 {
			issues = append(issues, prefix+".timeout_seconds cannot be negative")
		}
		if step.MaxRetries < 0 {
			issues = append(issues, prefix+".max_retries cannot be negative")
		}
		if step.Rollback != nil {
			if step.Rollback.Action == "" {
				issues = append(issues, prefix+".rollback.action is required when rollback is configured")
			}
			if step.Rollback.TimeoutSeconds < 0 {
				issues = append(issues, prefix+".rollback.timeout_seconds cannot be negative")
			}
		}

		totalOutcomes += len(step.ExpectedOutcomes)
	}

	for idx := range def.Steps {
		step := &def.Steps[idx]
		prefix := fmt.Sprintf("steps[%d]", idx)
		for outcomeIdx := range step.ExpectedOutcomes {
			outcomePrefix := fmt.Sprintf("%s.expected_outcomes[%d]", prefix, outcomeIdx)
			issues = append(issues, validateOutcome(outcomePrefix, &step.ExpectedOutcomes[outcomeIdx], stepIDs)...)
		}
	}

	for idx := range def.ExpectedOutcomes {
		prefix := fmt.Sprintf("expected_outcomes[%d]", idx)
		issues = append(issues, validateOutcome(prefix, &def.ExpectedOutcomes[idx], stepIDs)...)
	}

	if totalOutcomes == 0 {
		issues = append(issues, "at least one expected outcome is required (workflow-level or step-level)")
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

func normalizeDefinition(def *Definition) {
	def.Metadata.ID = strings.TrimSpace(strings.ToLower(def.Metadata.ID))
	def.Metadata.Name = strings.TrimSpace(def.Metadata.Name)
	def.Metadata.Version = strings.TrimSpace(def.Metadata.Version)
	def.Metadata.Description = strings.TrimSpace(def.Metadata.Description)

	for idx := range def.Inputs {
		def.Inputs[idx].Name = strings.TrimSpace(def.Inputs[idx].Name)
		def.Inputs[idx].Type = strings.TrimSpace(strings.ToLower(def.Inputs[idx].Type))
		def.Inputs[idx].Description = strings.TrimSpace(def.Inputs[idx].Description)
		def.Inputs[idx].Constraints.Pattern = strings.TrimSpace(def.Inputs[idx].Constraints.Pattern)
	}

	normalizeApproval(def.Approval)

	for idx := range def.Steps {
		def.Steps[idx].ID = strings.TrimSpace(def.Steps[idx].ID)
		def.Steps[idx].Name = strings.TrimSpace(def.Steps[idx].Name)
		def.Steps[idx].Description = strings.TrimSpace(def.Steps[idx].Description)
		def.Steps[idx].Action = strings.TrimSpace(def.Steps[idx].Action)
		if def.Steps[idx].Rollback != nil {
			def.Steps[idx].Rollback.Action = strings.TrimSpace(def.Steps[idx].Rollback.Action)
		}
		normalizeApproval(def.Steps[idx].Approval)
		for outcomeIdx := range def.Steps[idx].ExpectedOutcomes {
			normalizeOutcome(&def.Steps[idx].ExpectedOutcomes[outcomeIdx])
		}
	}

	for idx := range def.ExpectedOutcomes {
		normalizeOutcome(&def.ExpectedOutcomes[idx])
	}
}

func normalizeApproval(approval *ApprovalRequirement) {
	if approval == nil {
		return
	}
	approval.Policy = strings.TrimSpace(approval.Policy)
	approval.Reason = strings.TrimSpace(approval.Reason)
	for idx := range approval.ApproverRoles {
		approval.ApproverRoles[idx] = strings.TrimSpace(approval.ApproverRoles[idx])
	}
}

func normalizeOutcome(outcome *ExpectedOutcome) {
	if outcome == nil {
		return
	}
	outcome.ID = strings.TrimSpace(outcome.ID)
	outcome.Description = strings.TrimSpace(outcome.Description)
	outcome.SuccessCriteria = strings.TrimSpace(outcome.SuccessCriteria)
	outcome.StepID = strings.TrimSpace(outcome.StepID)
}

func validateApproval(path string, approval *ApprovalRequirement) string {
	if approval == nil {
		return ""
	}

	if approval.MinimumApprovers < 0 {
		return path + ".minimum_approvers cannot be negative"
	}
	if approval.Required && approval.MinimumApprovers == 0 {
		return path + ".minimum_approvers must be >= 1 when required=true"
	}
	if approval.MinimumApprovers > 0 && len(approval.ApproverRoles) > 0 && approval.MinimumApprovers > len(approval.ApproverRoles) {
		return path + ".minimum_approvers cannot exceed approver_roles length"
	}
	return ""
}

func validateInputConstraints(path string, in Input) []string {
	issues := make([]string, 0)
	c := in.Constraints

	switch in.Type {
	case InputTypeString:
		if c.MinLength != nil && *c.MinLength < 0 {
			issues = append(issues, path+".constraints.min_length cannot be negative")
		}
		if c.MaxLength != nil && *c.MaxLength < 0 {
			issues = append(issues, path+".constraints.max_length cannot be negative")
		}
		if c.MinLength != nil && c.MaxLength != nil && *c.MaxLength < *c.MinLength {
			issues = append(issues, path+".constraints.max_length must be >= min_length")
		}
		if c.Pattern != "" {
			if _, err := regexp.Compile(c.Pattern); err != nil {
				issues = append(issues, path+".constraints.pattern must be a valid regex")
			}
		}
		if c.Minimum != nil || c.Maximum != nil {
			issues = append(issues, path+".constraints.minimum/maximum only apply to number or integer")
		}
		if c.MinItems != nil || c.MaxItems != nil {
			issues = append(issues, path+".constraints.min_items/max_items only apply to array")
		}

	case InputTypeNumber, InputTypeInteger:
		if c.Minimum != nil && c.Maximum != nil && *c.Maximum < *c.Minimum {
			issues = append(issues, path+".constraints.maximum must be >= minimum")
		}
		if c.MinLength != nil || c.MaxLength != nil || c.Pattern != "" {
			issues = append(issues, path+".constraints.min_length/max_length/pattern only apply to string")
		}
		if c.MinItems != nil || c.MaxItems != nil {
			issues = append(issues, path+".constraints.min_items/max_items only apply to array")
		}

	case InputTypeBoolean:
		if c.MinLength != nil || c.MaxLength != nil || c.Pattern != "" || c.Minimum != nil || c.Maximum != nil || c.MinItems != nil || c.MaxItems != nil {
			issues = append(issues, path+".constraints are not supported for boolean inputs")
		}

	case InputTypeArray:
		if c.MinItems != nil && *c.MinItems < 0 {
			issues = append(issues, path+".constraints.min_items cannot be negative")
		}
		if c.MaxItems != nil && *c.MaxItems < 0 {
			issues = append(issues, path+".constraints.max_items cannot be negative")
		}
		if c.MinItems != nil && c.MaxItems != nil && *c.MaxItems < *c.MinItems {
			issues = append(issues, path+".constraints.max_items must be >= min_items")
		}
		if c.MinLength != nil || c.MaxLength != nil || c.Pattern != "" || c.Minimum != nil || c.Maximum != nil {
			issues = append(issues, path+".constraints.min_length/max_length/pattern/minimum/maximum do not apply to array")
		}

	case InputTypeObject:
		if c.MinLength != nil || c.MaxLength != nil || c.Pattern != "" || c.Minimum != nil || c.Maximum != nil || c.MinItems != nil || c.MaxItems != nil {
			issues = append(issues, path+".constraints are not supported for object inputs")
		}
	}

	if in.Default != nil && !valueMatchesType(in.Default, in.Type) {
		issues = append(issues, path+".default does not match declared input type")
	}

	for enumIdx, value := range c.Enum {
		if !valueMatchesType(value, in.Type) {
			issues = append(issues, fmt.Sprintf("%s.constraints.enum[%d] does not match declared input type", path, enumIdx))
		}
	}

	return issues
}

func validateOutcome(path string, outcome *ExpectedOutcome, stepIDs map[string]struct{}) []string {
	if outcome == nil {
		return []string{path + " is required"}
	}

	issues := make([]string, 0, 2)
	if outcome.Description == "" {
		issues = append(issues, path+".description is required")
	}
	if outcome.SuccessCriteria == "" {
		issues = append(issues, path+".success_criteria is required")
	}
	if outcome.StepID != "" {
		if _, ok := stepIDs[outcome.StepID]; !ok {
			issues = append(issues, path+".step_id must reference an existing step id")
		}
	}
	return issues
}

func isSupportedInputType(value string) bool {
	switch value {
	case InputTypeString, InputTypeNumber, InputTypeInteger, InputTypeBoolean, InputTypeArray, InputTypeObject:
		return true
	default:
		return false
	}
}

func valueMatchesType(value any, inputType string) bool {
	switch inputType {
	case InputTypeString:
		_, ok := value.(string)
		return ok
	case InputTypeBoolean:
		_, ok := value.(bool)
		return ok
	case InputTypeNumber:
		return isNumeric(value)
	case InputTypeInteger:
		return isInteger(value)
	case InputTypeArray:
		switch value.(type) {
		case []any:
			return true
		default:
			return false
		}
	case InputTypeObject:
		switch value.(type) {
		case map[string]any:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func isNumeric(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	default:
		return false
	}
}

func isInteger(v any) bool {
	switch value := v.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return math.Mod(float64(value), 1) == 0
	case float64:
		return math.Mod(value, 1) == 0
	default:
		return false
	}
}
