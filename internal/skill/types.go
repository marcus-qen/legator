/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skill

// Skill represents a loaded Agent Skill with parsed frontmatter and instructions.
type Skill struct {
	// Name is the skill identifier.
	Name string

	// Description is a human-readable summary.
	Description string

	// Version is the skill version (semver).
	Version string

	// License is the skill license.
	License string

	// Tags for categorization.
	Tags []string

	// Instructions is the markdown body (frontmatter stripped).
	Instructions string

	// Actions is the parsed Action Sheet (nil if no actions.yaml).
	Actions *ActionSheet

	// RawFrontmatter preserves the original YAML frontmatter.
	RawFrontmatter map[string]interface{}
}

// ActionSheet represents a parsed actions.yaml file.
type ActionSheet struct {
	// Actions is the list of declared actions.
	Actions []Action `yaml:"actions"`
}

// Action declares a single permitted operation.
type Action struct {
	// ID uniquely identifies this action within the skill.
	ID string `yaml:"id"`

	// Description is a human-readable summary.
	Description string `yaml:"description"`

	// Tool is the tool identifier (e.g. "kubectl.get", "http.post").
	Tool string `yaml:"tool"`

	// TargetPattern is a glob pattern for the tool target.
	TargetPattern string `yaml:"targetPattern"`

	// Tier is the risk classification.
	Tier string `yaml:"tier"`

	// SideEffects describes the side effect category.
	SideEffects string `yaml:"sideEffects"`

	// PreConditions are checks that must pass before execution.
	PreConditions []PreCondition `yaml:"preConditions"`

	// Rollback describes how to undo this action.
	Rollback *RollbackSpec `yaml:"rollback,omitempty"`

	// Cooldown is the minimum time between executions (e.g. "300s").
	Cooldown string `yaml:"cooldown,omitempty"`

	// DataImpact describes data risk level.
	DataImpact string `yaml:"dataImpact"`

	// RuntimeOverride allows the runtime to unconditionally block this action.
	RuntimeOverride string `yaml:"runtimeOverride,omitempty"`
}

// PreCondition is a check that runs before an action.
type PreCondition struct {
	Check      string `yaml:"check"`
	FailAction string `yaml:"failAction"`
}

// RollbackSpec describes how to undo an action.
type RollbackSpec struct {
	Description string `yaml:"description"`
	Automatic   bool   `yaml:"automatic"`
	Command     string `yaml:"command,omitempty"`
}
