package automationpacks

import "testing"

func TestValidateDefinitionAcceptsValidSchema(t *testing.T) {
	def := validDefinitionFixture()

	if err := ValidateDefinition(&def); err != nil {
		t.Fatalf("expected valid schema, got err=%v", err)
	}
	if def.Metadata.ID != "pack.backup-db" {
		t.Fatalf("expected normalized id, got %q", def.Metadata.ID)
	}
	if def.Inputs[0].Type != InputTypeString {
		t.Fatalf("expected normalized input type, got %q", def.Inputs[0].Type)
	}
}

func TestValidateDefinitionRejectsMissingMetadataAndOutcomes(t *testing.T) {
	def := Definition{
		Metadata: Metadata{},
		Steps:    []Step{{ID: "s1", Action: "run_command"}},
	}

	err := ValidateDefinition(&def)
	if err == nil {
		t.Fatal("expected validation error")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if len(validationErr.Issues) < 4 {
		t.Fatalf("expected multiple validation issues, got %+v", validationErr.Issues)
	}
}

func TestValidateDefinitionRejectsConstraintTypeMismatch(t *testing.T) {
	def := validDefinitionFixture()
	def.Inputs[0].Constraints.Minimum = ptrFloat(1)

	err := ValidateDefinition(&def)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateDefinitionRejectsDefaultTypeMismatch(t *testing.T) {
	def := validDefinitionFixture()
	def.Inputs[0].Default = true

	err := ValidateDefinition(&def)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateDefinitionRejectsDuplicateStepIDAndUnknownOutcomeStep(t *testing.T) {
	def := validDefinitionFixture()
	def.Steps[1].ID = def.Steps[0].ID
	def.ExpectedOutcomes[0].StepID = "missing-step"

	err := ValidateDefinition(&def)
	if err == nil {
		t.Fatal("expected validation error")
	}

	validationErr := err.(*ValidationError)
	if len(validationErr.Issues) < 2 {
		t.Fatalf("expected duplicate-id and unknown-step issues, got %+v", validationErr.Issues)
	}
}

func TestValidateDefinitionRejectsInvalidExecutionFields(t *testing.T) {
	def := validDefinitionFixture()
	def.Steps[0].TimeoutSeconds = -1
	def.Steps[0].MaxRetries = -1
	def.Steps[0].Rollback = &RollbackHook{Action: "", TimeoutSeconds: -5}

	err := ValidateDefinition(&def)
	if err == nil {
		t.Fatal("expected validation error")
	}

	validationErr := err.(*ValidationError)
	if len(validationErr.Issues) < 3 {
		t.Fatalf("expected execution-field validation issues, got %+v", validationErr.Issues)
	}
}

func validDefinitionFixture() Definition {
	minLen := 3
	maxLen := 32
	return Definition{
		Metadata: Metadata{
			ID:          " Pack.Backup-DB ",
			Name:        "Backup DB",
			Version:     "1.0.0",
			Description: "Nightly DB backup workflow",
		},
		Inputs: []Input{
			{
				Name:        "environment",
				Type:        " STRING ",
				Required:    true,
				Default:     "prod",
				Description: "Target environment",
				Constraints: InputConstraints{
					MinLength: &minLen,
					MaxLength: &maxLen,
					Enum:      []any{"prod", "staging"},
				},
			},
		},
		Approval: &ApprovalRequirement{
			Required:         true,
			MinimumApprovers: 1,
			ApproverRoles:    []string{"ops", "security"},
		},
		Steps: []Step{
			{
				ID:     "prepare",
				Action: "run_command",
				Parameters: map[string]any{
					"command": "pg_dump --schema-only",
				},
			},
			{
				ID:     "archive",
				Action: "upload_artifact",
				Parameters: map[string]any{
					"bucket": "legator-backups",
				},
				Approval: &ApprovalRequirement{
					Required:         true,
					MinimumApprovers: 1,
					ApproverRoles:    []string{"ops"},
				},
				ExpectedOutcomes: []ExpectedOutcome{
					{
						Description:     "Archive uploaded",
						SuccessCriteria: "artifact_uri is present",
						Required:        true,
					},
				},
			},
		},
		ExpectedOutcomes: []ExpectedOutcome{
			{
				Description:     "Workflow completes",
				SuccessCriteria: "all required steps are successful",
				StepID:          "archive",
				Required:        true,
			},
		},
	}
}

func ptrFloat(v float64) *float64 {
	return &v
}
