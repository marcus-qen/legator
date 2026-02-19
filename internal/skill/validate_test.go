/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skill

import (
	"testing"
)

func TestValidate_ValidSkill(t *testing.T) {
	skill := &Skill{
		Name:         "endpoint-monitoring",
		Description:  "Fast endpoint health probe",
		Version:      "1.0.0",
		License:      "Apache-2.0",
		Tags:         []string{"monitoring"},
		Instructions: "# Check endpoints\nVerify all endpoints are responding.",
	}

	result := Validate(skill)
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

func TestValidate_MissingName(t *testing.T) {
	skill := &Skill{
		Description:  "test",
		Instructions: "do stuff",
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for missing name")
	}
	if len(result.Errors) < 1 {
		t.Error("expected at least 1 error")
	}
}

func TestValidate_MissingDescription(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Instructions: "do stuff",
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for missing description")
	}
}

func TestValidate_EmptyInstructions(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "",
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for empty instructions")
	}
}

func TestValidate_WhitespaceOnlyInstructions(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "   \n\t  ",
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for whitespace-only instructions")
	}
}

func TestValidate_Nil(t *testing.T) {
	result := Validate(nil)
	if result.Valid {
		t.Error("expected invalid for nil skill")
	}
}

func TestValidate_Warnings(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test skill",
		Instructions: "do stuff",
		// Missing: version, license, tags
	}

	result := Validate(skill)
	if !result.Valid {
		t.Errorf("should be valid despite warnings: %v", result.Errors)
	}
	if len(result.Warnings) < 3 {
		t.Errorf("expected at least 3 warnings (version, license, tags), got %d: %v",
			len(result.Warnings), result.Warnings)
	}
}

func TestValidate_ActionSheet(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "do stuff",
		Actions: &ActionSheet{
			Actions: []Action{
				{ID: "check", Tool: "kubectl.get", Tier: "read", Description: "get pods"},
				{ID: "restart", Tool: "kubectl.rollout", Tier: "service-mutation"},
			},
		},
	}

	result := Validate(skill)
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
	// Second action missing description → warning
	foundWarning := false
	for _, w := range result.Warnings {
		if w == "actions[1] (restart): missing description" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected warning for missing action description, got: %v", result.Warnings)
	}
}

func TestValidate_ActionSheetMissingID(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "do stuff",
		Actions: &ActionSheet{
			Actions: []Action{
				{Tool: "kubectl.get", Tier: "read"},
			},
		},
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for action missing ID")
	}
}

func TestValidate_ActionSheetMissingTool(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "do stuff",
		Actions: &ActionSheet{
			Actions: []Action{
				{ID: "check", Tier: "read"},
			},
		},
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for action missing tool")
	}
}

func TestValidate_ActionSheetInvalidTier(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "do stuff",
		Actions: &ActionSheet{
			Actions: []Action{
				{ID: "check", Tool: "kubectl.get", Tier: "nuclear"},
			},
		},
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for invalid tier")
	}
}

func TestValidate_DuplicateActionIDs(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "do stuff",
		Actions: &ActionSheet{
			Actions: []Action{
				{ID: "check", Tool: "kubectl.get", Tier: "read"},
				{ID: "check", Tool: "http.get", Tier: "read"},
			},
		},
	}

	result := Validate(skill)
	if result.Valid {
		t.Error("expected invalid for duplicate action IDs")
	}
}

func TestMustValidate_Valid(t *testing.T) {
	skill := &Skill{
		Name:         "test",
		Description:  "test",
		Instructions: "do stuff",
	}

	err := MustValidate(skill)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestMustValidate_Invalid(t *testing.T) {
	skill := &Skill{} // Missing everything

	err := MustValidate(skill)
	if err == nil {
		t.Error("expected error for invalid skill")
	}
}
