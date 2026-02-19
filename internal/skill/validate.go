/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skill

import (
	"fmt"
	"strings"
)

// ValidationResult holds the outcome of skill validation.
type ValidationResult struct {
	// Valid is true if the skill passes all required checks.
	Valid bool

	// Errors are fatal issues that prevent the skill from being used.
	Errors []string

	// Warnings are non-fatal issues that should be addressed.
	Warnings []string
}

// Validate checks a loaded skill for required fields and common issues.
// Returns a ValidationResult with errors (fatal) and warnings (non-fatal).
func Validate(skill *Skill) *ValidationResult {
	result := &ValidationResult{Valid: true}

	if skill == nil {
		result.Valid = false
		result.Errors = append(result.Errors, "skill is nil")
		return result
	}

	// Required fields
	if skill.Name == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "missing required field: name")
	}

	if skill.Description == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "missing required field: description")
	}

	if skill.Version == "" {
		result.Warnings = append(result.Warnings, "missing field: version (recommended for reproducibility)")
	}

	if strings.TrimSpace(skill.Instructions) == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "skill has no instructions (empty SKILL.md body)")
	}

	// Action Sheet validation
	if skill.Actions != nil {
		for i, action := range skill.Actions.Actions {
			prefix := fmt.Sprintf("actions[%d]", i)

			if action.ID == "" {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("%s: missing required field: id", prefix))
			}

			if action.Tool == "" {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("%s: missing required field: tool", prefix))
			}

			if action.Tier == "" {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("%s: missing required field: tier", prefix))
			} else {
				validTiers := map[string]bool{
					"read": true, "service-mutation": true,
					"destructive-mutation": true, "data-mutation": true,
				}
				if !validTiers[action.Tier] {
					result.Valid = false
					result.Errors = append(result.Errors,
						fmt.Sprintf("%s: invalid tier %q (must be read|service-mutation|destructive-mutation|data-mutation)", prefix, action.Tier))
				}
			}

			if action.Description == "" {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("%s (%s): missing description", prefix, action.ID))
			}
		}

		// Check for duplicate action IDs
		seen := make(map[string]bool)
		for _, action := range skill.Actions.Actions {
			if action.ID != "" {
				if seen[action.ID] {
					result.Valid = false
					result.Errors = append(result.Errors,
						fmt.Sprintf("duplicate action ID: %q", action.ID))
				}
				seen[action.ID] = true
			}
		}
	}

	// Warnings for optional but recommended fields
	if skill.License == "" {
		result.Warnings = append(result.Warnings, "missing field: license")
	}

	if len(skill.Tags) == 0 {
		result.Warnings = append(result.Warnings, "missing field: tags (helps with discovery)")
	}

	return result
}

// MustValidate validates a skill and returns an error if it's invalid.
func MustValidate(skill *Skill) error {
	result := Validate(skill)
	if !result.Valid {
		return fmt.Errorf("skill validation failed: %s", strings.Join(result.Errors, "; "))
	}
	return nil
}
