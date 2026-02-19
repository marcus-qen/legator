/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package assembler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
	"github.com/marcus-qen/infraagent/internal/resolver"
	"github.com/marcus-qen/infraagent/internal/skill"
)

// AssembledAgent is the complete output of the assembly process —
// everything needed to execute an agent run.
type AssembledAgent struct {
	// Prompt is the complete system prompt for the LLM.
	Prompt string

	// Model is the resolved LLM model details.
	Model *resolver.ResolvedModel

	// Environment is the resolved environment.
	Environment *resolver.ResolvedEnvironment

	// Skills is the list of loaded skills.
	Skills []*skill.Skill

	// ActionRegistry maps action IDs to their declarations.
	ActionRegistry map[string]*skill.Action

	// CapabilityCheck is the result of capability validation.
	CapabilityCheck *resolver.CapabilityCheckResult

	// Warnings is a list of non-fatal assembly warnings.
	Warnings []string

	// Agent is a reference to the source InfraAgent.
	Agent *corev1alpha1.InfraAgent
}

// Assembler combines InfraAgent + Skills + AgentEnvironment into a runnable agent.
type Assembler struct {
	client client.Client
}

// New creates a new Assembler.
func New(c client.Client) *Assembler {
	return &Assembler{client: c}
}

// Assemble loads all components and produces an AssembledAgent.
func (a *Assembler) Assemble(ctx context.Context, agent *corev1alpha1.InfraAgent) (*AssembledAgent, error) {
	result := &AssembledAgent{
		Agent:          agent,
		ActionRegistry: make(map[string]*skill.Action),
	}

	// 1. Resolve environment
	envResolver := resolver.NewEnvironmentResolver(a.client, agent.Namespace)
	env, err := envResolver.Resolve(ctx, agent.Spec.EnvironmentRef)
	if err != nil {
		return nil, fmt.Errorf("environment resolution failed: %w", err)
	}
	result.Environment = env

	// 2. Resolve model tier
	modelResolver := resolver.NewModelTierResolver(a.client)
	model, err := modelResolver.Resolve(ctx, agent.Spec.Model.Tier)
	if err != nil {
		return nil, fmt.Errorf("model tier resolution failed: %w", err)
	}
	result.Model = model

	// 3. Load skills
	skillLoader := skill.NewLoader(a.client, agent.Namespace)
	for _, skillRef := range agent.Spec.Skills {
		s, err := skillLoader.Load(ctx, skillRef.Name, skillRef.Source)
		if err != nil {
			return nil, fmt.Errorf("skill loading failed for %q: %w", skillRef.Name, err)
		}
		result.Skills = append(result.Skills, s)

		// Register actions from Action Sheet
		if s.Actions != nil {
			for i := range s.Actions.Actions {
				action := &s.Actions.Actions[i]
				result.ActionRegistry[action.ID] = action
			}
		}
	}

	// 4. Validate capabilities
	capCheck := resolver.ValidateCapabilities(agent.Spec.Capabilities, env)
	result.CapabilityCheck = capCheck
	if !capCheck.Satisfied {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("unsatisfied capabilities: %s", strings.Join(capCheck.Missing, ", ")))
	}
	if len(capCheck.OptionalMissing) > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("optional capabilities unavailable: %s", strings.Join(capCheck.OptionalMissing, ", ")))
	}

	// 5. Validate Action Sheets against guardrails
	actionWarnings := validateActionsAgainstGuardrails(result.ActionRegistry, &agent.Spec.Guardrails)
	result.Warnings = append(result.Warnings, actionWarnings...)

	// 6. Assemble prompt
	result.Prompt = buildPrompt(agent, result.Skills, env, model)

	return result, nil
}

// buildPrompt constructs the complete system prompt from all components.
func buildPrompt(
	agent *corev1alpha1.InfraAgent,
	skills []*skill.Skill,
	env *resolver.ResolvedEnvironment,
	model *resolver.ResolvedModel,
) string {
	var b strings.Builder

	// Identity
	emoji := agent.Spec.Emoji
	if emoji == "" {
		emoji = "🤖"
	}
	fmt.Fprintf(&b, "You are %s %s — %s\n\n", emoji, agent.Name, agent.Spec.Description)

	// Guardrails preamble
	b.WriteString(buildGuardrailsSection(&agent.Spec.Guardrails))
	b.WriteString("\n")

	// Reporting rules
	b.WriteString(buildReportingSection(agent.Spec.Reporting))
	b.WriteString("\n")

	// Environment context
	b.WriteString(buildEnvironmentSection(env))
	b.WriteString("\n")

	// Skill instructions
	for _, s := range skills {
		if s.Instructions != "" {
			b.WriteString(s.Instructions)
			b.WriteString("\n\n")
		}
	}

	// Closing — metrics and audit
	b.WriteString("## Run Completion (MANDATORY)\n")
	b.WriteString("Before finishing, emit structured metrics:\n")
	b.WriteString("- `agent.run.status`: ok | error\n")
	b.WriteString("- `agent.run.duration_ms`: wall-clock time\n")
	b.WriteString("- Summary of actions taken and findings\n")

	return b.String()
}

// buildGuardrailsSection creates the guardrails preamble.
func buildGuardrailsSection(g *corev1alpha1.GuardrailsSpec) string {
	var b strings.Builder
	b.WriteString("## Guardrails (enforced by runtime)\n")
	fmt.Fprintf(&b, "- Autonomy level: %s\n", g.Autonomy)
	fmt.Fprintf(&b, "- Max iterations: %d\n", g.MaxIterations)
	fmt.Fprintf(&b, "- Max retries: %d\n", g.MaxRetries)

	if g.Escalation != nil {
		fmt.Fprintf(&b, "- Escalation target: %s\n", g.Escalation.Target)
		if g.Escalation.Timeout != "" {
			fmt.Fprintf(&b, "- Escalation timeout: %s\n", g.Escalation.Timeout)
		}
	}

	switch g.Autonomy {
	case corev1alpha1.AutonomyObserve:
		b.WriteString("\n**You are READ-ONLY. Do not attempt any mutations.**\n")
	case corev1alpha1.AutonomyRecommend:
		b.WriteString("\n**You may recommend actions but must not execute mutations. Report recommendations for human review.**\n")
	case corev1alpha1.AutonomySafe:
		b.WriteString("\n**You may execute safe (reversible) mutations. Destructive operations require escalation.**\n")
	case corev1alpha1.AutonomyDestructive:
		b.WriteString("\n**Full autonomy for service operations. Data mutations are ALWAYS blocked by the runtime regardless of autonomy level.**\n")
	}

	if len(g.AllowedActions) > 0 {
		b.WriteString("\n### Allowed Actions\n")
		for _, a := range g.AllowedActions {
			fmt.Fprintf(&b, "- `%s`\n", a)
		}
	}

	if len(g.DeniedActions) > 0 {
		b.WriteString("\n### Denied Actions (always blocked)\n")
		for _, a := range g.DeniedActions {
			fmt.Fprintf(&b, "- `%s`\n", a)
		}
	}

	return b.String()
}

// buildReportingSection creates the reporting rules.
func buildReportingSection(r *corev1alpha1.ReportingSpec) string {
	var b strings.Builder
	b.WriteString("## Reporting Rules\n")

	if r == nil {
		b.WriteString("- On success: silent\n")
		b.WriteString("- On failure: escalate\n")
		b.WriteString("- On finding: log\n")
	} else {
		fmt.Fprintf(&b, "- On success: %s\n", r.OnSuccess)
		fmt.Fprintf(&b, "- On failure: %s\n", r.OnFailure)
		fmt.Fprintf(&b, "- On finding: %s\n", r.OnFinding)
	}

	b.WriteString("\nIf onSuccess is 'silent': reply NO_REPLY when everything is OK.\n")
	return b.String()
}

// buildEnvironmentSection creates the environment context block.
func buildEnvironmentSection(env *resolver.ResolvedEnvironment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Environment: %s\n\n", env.Name)

	// Endpoints
	if len(env.Endpoints) > 0 {
		b.WriteString("### Endpoints\n")
		// Sort for deterministic output
		names := make([]string, 0, len(env.Endpoints))
		for name := range env.Endpoints {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			ep := env.Endpoints[name]
			flags := ""
			if ep.Internal {
				flags = " (internal)"
			}
			healthSuffix := ""
			if ep.HealthPath != "" {
				healthSuffix = ep.HealthPath
			}
			fmt.Fprintf(&b, "- %s: %s%s%s\n", name, ep.URL, healthSuffix, flags)
		}
		b.WriteString("\n")
	}

	// Namespaces
	if env.Namespaces != nil {
		b.WriteString("### Namespaces\n")
		if len(env.Namespaces.Monitoring) > 0 {
			fmt.Fprintf(&b, "- monitoring: %s\n", strings.Join(env.Namespaces.Monitoring, ", "))
		}
		if len(env.Namespaces.Apps) > 0 {
			fmt.Fprintf(&b, "- apps: %s\n", strings.Join(env.Namespaces.Apps, ", "))
		}
		if len(env.Namespaces.System) > 0 {
			fmt.Fprintf(&b, "- system: %s\n", strings.Join(env.Namespaces.System, ", "))
		}
		b.WriteString("\n")
	}

	// MCP Servers
	if len(env.MCPServers) > 0 {
		b.WriteString("### Available Tool Servers (MCP)\n")
		for name, mcp := range env.MCPServers {
			fmt.Fprintf(&b, "- %s: %s", name, mcp.Endpoint)
			if len(mcp.Capabilities) > 0 {
				fmt.Fprintf(&b, " [%s]", strings.Join(mcp.Capabilities, ", "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Data resources warning
	if env.DataResources != nil {
		hasData := len(env.DataResources.Databases) > 0 ||
			len(env.DataResources.PersistentStorage) > 0 ||
			len(env.DataResources.ObjectStorage) > 0
		if hasData {
			b.WriteString("### ⚠️ Data Resources (protected)\n")
			b.WriteString("The following resources contain critical data. ")
			b.WriteString("Operations affecting these resources or their namespaces ")
			b.WriteString("are subject to additional pre-flight checks and may be blocked.\n")
			for _, db := range env.DataResources.Databases {
				fmt.Fprintf(&b, "- Database: %s/%s (%s)\n", db.Namespace, db.Name, db.Kind)
			}
			for _, ps := range env.DataResources.PersistentStorage {
				fmt.Fprintf(&b, "- Storage: %s/%s (%s)\n", ps.Namespace, ps.Name, ps.Kind)
			}
			for _, os := range env.DataResources.ObjectStorage {
				fmt.Fprintf(&b, "- Object store: %s (%s)\n", os.Name, os.Kind)
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// validateActionsAgainstGuardrails checks for mismatches between
// declared actions and the agent's guardrail configuration.
func validateActionsAgainstGuardrails(
	registry map[string]*skill.Action,
	guardrails *corev1alpha1.GuardrailsSpec,
) []string {
	var warnings []string

	for id, action := range registry {
		// Check if action would be blocked by autonomy level
		switch action.Tier {
		case "service-mutation":
			if guardrails.Autonomy == corev1alpha1.AutonomyObserve ||
				guardrails.Autonomy == corev1alpha1.AutonomyRecommend {
				warnings = append(warnings,
					fmt.Sprintf("action %q (tier: service-mutation) will be blocked: agent autonomy is %s",
						id, guardrails.Autonomy))
			}
		case "destructive-mutation":
			if guardrails.Autonomy != corev1alpha1.AutonomyDestructive {
				warnings = append(warnings,
					fmt.Sprintf("action %q (tier: destructive-mutation) will be blocked: agent autonomy is %s",
						id, guardrails.Autonomy))
			}
		case "data-mutation":
			warnings = append(warnings,
				fmt.Sprintf("action %q (tier: data-mutation) will always be blocked by runtime data protection",
					id))
		}

		// Check against deny list
		for _, denied := range guardrails.DeniedActions {
			if matchGlob(denied, action.Tool+" "+action.TargetPattern) {
				warnings = append(warnings,
					fmt.Sprintf("action %q matches deny list pattern %q", id, denied))
			}
		}
	}

	return warnings
}

// matchGlob performs simple glob matching (* matches any sequence).
func matchGlob(pattern, text string) bool {
	// Simple glob: split on *, check if all parts appear in order
	parts := strings.Split(pattern, "*")
	remaining := text
	for _, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(remaining, part)
		if idx < 0 {
			return false
		}
		remaining = remaining[idx+len(part):]
	}
	return true
}
