package automationpacks

import "time"

const (
	InputTypeString  = "string"
	InputTypeNumber  = "number"
	InputTypeInteger = "integer"
	InputTypeBoolean = "boolean"
	InputTypeArray   = "array"
	InputTypeObject  = "object"
)

// Definition is a machine-readable automation pack/workflow schema.
type Definition struct {
	Metadata         Metadata             `json:"metadata"`
	Inputs           []Input              `json:"inputs,omitempty"`
	Approval         *ApprovalRequirement `json:"approval,omitempty"`
	Steps            []Step               `json:"steps"`
	ExpectedOutcomes []ExpectedOutcome    `json:"expected_outcomes,omitempty"`
	CreatedAt        time.Time            `json:"created_at,omitempty"`
	UpdatedAt        time.Time            `json:"updated_at,omitempty"`
}

// Metadata identifies an automation pack definition.
type Metadata struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// Input defines one typed workflow input.
type Input struct {
	Name        string           `json:"name"`
	Type        string           `json:"type"`
	Description string           `json:"description,omitempty"`
	Required    bool             `json:"required,omitempty"`
	Default     any              `json:"default,omitempty"`
	Constraints InputConstraints `json:"constraints,omitempty"`
}

// InputConstraints are type-specific validation constraints.
type InputConstraints struct {
	MinLength *int     `json:"min_length,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	Enum      []any    `json:"enum,omitempty"`
	MinItems  *int     `json:"min_items,omitempty"`
	MaxItems  *int     `json:"max_items,omitempty"`
}

// Step is one ordered workflow step.
type Step struct {
	ID               string               `json:"id"`
	Name             string               `json:"name,omitempty"`
	Description      string               `json:"description,omitempty"`
	Action           string               `json:"action"`
	Parameters       map[string]any       `json:"parameters,omitempty"`
	Approval         *ApprovalRequirement `json:"approval,omitempty"`
	ExpectedOutcomes []ExpectedOutcome    `json:"expected_outcomes,omitempty"`
}

// ApprovalRequirement captures workflow or step approval requirements.
type ApprovalRequirement struct {
	Required         bool     `json:"required"`
	Policy           string   `json:"policy,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	MinimumApprovers int      `json:"minimum_approvers,omitempty"`
	ApproverRoles    []string `json:"approver_roles,omitempty"`
}

// ExpectedOutcome captures machine-readable success criteria.
type ExpectedOutcome struct {
	ID              string `json:"id,omitempty"`
	Description     string `json:"description"`
	SuccessCriteria string `json:"success_criteria"`
	StepID          string `json:"step_id,omitempty"`
	Required        bool   `json:"required,omitempty"`
}

// DefinitionSummary is the listing shape for automation packs.
type DefinitionSummary struct {
	Metadata  Metadata  `json:"metadata"`
	InputCount int      `json:"input_count"`
	StepCount  int      `json:"step_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
