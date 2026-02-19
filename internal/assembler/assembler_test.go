/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package assembler

import (
	"strings"
	"testing"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
	"github.com/marcus-qen/infraagent/internal/resolver"
	"github.com/marcus-qen/infraagent/internal/skill"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testAgent() *corev1alpha1.InfraAgent {
	return &corev1alpha1.InfraAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "watchman-light",
			Namespace: "agents",
		},
		Spec: corev1alpha1.InfraAgentSpec{
			Description: "Fast endpoint probe and critical alert check",
			Emoji:       "👁️",
			Schedule: corev1alpha1.ScheduleSpec{
				Cron:     "*/5 * * * *",
				Timezone: "UTC",
			},
			Model: corev1alpha1.ModelSpec{
				Tier:        corev1alpha1.ModelTierFast,
				TokenBudget: 8000,
				Timeout:     "60s",
			},
			Skills: []corev1alpha1.SkillRef{
				{Name: "endpoint-monitoring", Source: "bundled"},
			},
			Guardrails: corev1alpha1.GuardrailsSpec{
				Autonomy: corev1alpha1.AutonomySafe,
				AllowedActions: []string{
					"http.get *",
					"kubectl.get *",
					"kubectl.rollout restart deployment/*",
				},
				DeniedActions: []string{
					"kubectl.delete namespace/*",
					"kubectl.scale *",
				},
				Escalation: &corev1alpha1.EscalationSpec{
					Target:    corev1alpha1.EscalationParent,
					Timeout:   "0s",
					OnTimeout: corev1alpha1.TimeoutCancel,
				},
				MaxIterations: 5,
				MaxRetries:    1,
			},
			Reporting: &corev1alpha1.ReportingSpec{
				OnSuccess: corev1alpha1.ReportSilent,
				OnFailure: corev1alpha1.ReportEscalate,
				OnFinding: corev1alpha1.ReportLog,
			},
			EnvironmentRef: "dev-lab",
		},
	}
}

func testEnvironment() *resolver.ResolvedEnvironment {
	return &resolver.ResolvedEnvironment{
		Name: "dev-lab",
		Endpoints: map[string]corev1alpha1.EndpointSpec{
			"alertmanager": {URL: "http://alertmanager.monitoring:9093", HealthPath: "/-/healthy", Internal: true},
			"backstage":    {URL: "https://backstage.example.com", HealthPath: "/healthcheck"},
			"grafana":      {URL: "https://grafana.example.com", HealthPath: "/api/health"},
		},
		Namespaces: &corev1alpha1.NamespaceMap{
			Monitoring: []string{"monitoring"},
			Apps:       []string{"backstage", "ideavault"},
			System:     []string{"kube-system", "cert-manager"},
		},
		DataResources: &corev1alpha1.DataResourcesSpec{
			Databases: []corev1alpha1.DataResourceRef{
				{Kind: "cnpg.io/Cluster", Namespace: "backstage", Name: "backstage-db"},
			},
			PersistentStorage: []corev1alpha1.DataResourceRef{
				{Kind: "PersistentVolumeClaim", Namespace: "harbor", Name: "harbor-registry"},
			},
		},
		MCPServers: map[string]corev1alpha1.MCPServerSpec{
			"k8sgpt": {Endpoint: "http://k8sgpt:8089", Capabilities: []string{"k8sgpt.analyze"}},
		},
	}
}

func testModel() *resolver.ResolvedModel {
	return &resolver.ResolvedModel{
		Tier:            corev1alpha1.ModelTierFast,
		Provider:        "anthropic",
		Model:           "claude-haiku-3-5-20241022",
		FullModelString: "anthropic/claude-haiku-3-5-20241022",
	}
}

func testSkills() []*skill.Skill {
	return []*skill.Skill{
		{
			Name:         "endpoint-monitoring",
			Description:  "Fast endpoint health probe",
			Version:      "1.0.0",
			Instructions: "# Endpoint Monitoring\n\nCheck all endpoints are responding.\n\n## Step 1: Probe\nHTTP GET each endpoint.",
		},
	}
}

func TestBuildPrompt_ContainsIdentity(t *testing.T) {
	agent := testAgent()
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "👁️ watchman-light") {
		t.Error("prompt should contain agent identity with emoji")
	}
	if !strings.Contains(prompt, "Fast endpoint probe") {
		t.Error("prompt should contain description")
	}
}

func TestBuildPrompt_ContainsGuardrails(t *testing.T) {
	agent := testAgent()
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "Autonomy level: automate-safe") {
		t.Error("prompt should contain autonomy level")
	}
	if !strings.Contains(prompt, "Max iterations: 5") {
		t.Error("prompt should contain max iterations")
	}
	if !strings.Contains(prompt, "safe (reversible) mutations") {
		t.Error("prompt should explain automate-safe level")
	}
	if !strings.Contains(prompt, "http.get *") {
		t.Error("prompt should contain allowed actions")
	}
	if !strings.Contains(prompt, "kubectl.delete namespace/*") {
		t.Error("prompt should contain denied actions")
	}
}

func TestBuildPrompt_ContainsEnvironment(t *testing.T) {
	agent := testAgent()
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "Environment: dev-lab") {
		t.Error("prompt should contain environment name")
	}
	if !strings.Contains(prompt, "alertmanager:") {
		t.Error("prompt should contain endpoints")
	}
	if !strings.Contains(prompt, "(internal)") {
		t.Error("prompt should flag internal endpoints")
	}
	if !strings.Contains(prompt, "backstage, ideavault") {
		t.Error("prompt should contain app namespaces")
	}
}

func TestBuildPrompt_ContainsDataResources(t *testing.T) {
	agent := testAgent()
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "Data Resources (protected)") {
		t.Error("prompt should warn about data resources")
	}
	if !strings.Contains(prompt, "backstage-db") {
		t.Error("prompt should list databases")
	}
	if !strings.Contains(prompt, "harbor-registry") {
		t.Error("prompt should list persistent storage")
	}
}

func TestBuildPrompt_ContainsSkillInstructions(t *testing.T) {
	agent := testAgent()
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "# Endpoint Monitoring") {
		t.Error("prompt should contain skill instructions")
	}
	if !strings.Contains(prompt, "HTTP GET each endpoint") {
		t.Error("prompt should contain skill body")
	}
}

func TestBuildPrompt_ContainsReporting(t *testing.T) {
	agent := testAgent()
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "On success: silent") {
		t.Error("prompt should contain reporting rules")
	}
	if !strings.Contains(prompt, "NO_REPLY") {
		t.Error("prompt should mention NO_REPLY for silent success")
	}
}

func TestBuildPrompt_ContainsMCPServers(t *testing.T) {
	agent := testAgent()
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "k8sgpt") {
		t.Error("prompt should list MCP servers")
	}
	if !strings.Contains(prompt, "k8sgpt.analyze") {
		t.Error("prompt should list MCP capabilities")
	}
}

func TestBuildPrompt_ObserveMode(t *testing.T) {
	agent := testAgent()
	agent.Spec.Guardrails.Autonomy = corev1alpha1.AutonomyObserve
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "READ-ONLY") {
		t.Error("observe mode should say READ-ONLY")
	}
}

func TestBuildPrompt_DestructiveMode(t *testing.T) {
	agent := testAgent()
	agent.Spec.Guardrails.Autonomy = corev1alpha1.AutonomyDestructive
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "Data mutations are ALWAYS blocked") {
		t.Error("destructive mode should still warn about data protection")
	}
}

func TestBuildPrompt_NoEmoji(t *testing.T) {
	agent := testAgent()
	agent.Spec.Emoji = ""
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "🤖 watchman-light") {
		t.Error("missing emoji should default to 🤖")
	}
}

func TestBuildPrompt_NilReporting(t *testing.T) {
	agent := testAgent()
	agent.Spec.Reporting = nil
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "On success: silent") {
		t.Error("nil reporting should use defaults")
	}
}

func TestValidateActionsAgainstGuardrails_ServiceMutationBlocked(t *testing.T) {
	registry := map[string]*skill.Action{
		"restart": {ID: "restart", Tier: "service-mutation", Tool: "kubectl.rollout"},
	}
	guardrails := &corev1alpha1.GuardrailsSpec{
		Autonomy: corev1alpha1.AutonomyObserve,
	}

	warnings := validateActionsAgainstGuardrails(registry, guardrails)
	if len(warnings) == 0 {
		t.Error("expected warning about service-mutation at observe level")
	}
}

func TestValidateActionsAgainstGuardrails_DataMutationAlwaysWarns(t *testing.T) {
	registry := map[string]*skill.Action{
		"delete-pvc": {ID: "delete-pvc", Tier: "data-mutation", Tool: "kubectl.delete"},
	}
	guardrails := &corev1alpha1.GuardrailsSpec{
		Autonomy: corev1alpha1.AutonomyDestructive,
	}

	warnings := validateActionsAgainstGuardrails(registry, guardrails)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "data-mutation") && strings.Contains(w, "always be blocked") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected data-mutation warning, got: %v", warnings)
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		text    string
		want    bool
	}{
		{"kubectl.get *", "kubectl.get pods -n backstage", true},
		{"kubectl.delete namespace/*", "kubectl.delete namespace/backstage", true},
		{"http.get *", "kubectl.get pods", false},
		{"*", "anything", true},
		{"kubectl.scale *", "kubectl.scale deployment/x --replicas=0", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.text, func(t *testing.T) {
			got := matchGlob(tt.pattern, tt.text)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.text, got, tt.want)
			}
		})
	}
}

func TestBuildPrompt_RecommendMode(t *testing.T) {
	agent := testAgent()
	agent.Spec.Guardrails.Autonomy = corev1alpha1.AutonomyRecommend
	prompt := buildPrompt(agent, testSkills(), testEnvironment(), testModel())

	if !strings.Contains(prompt, "recommend actions but must not execute") {
		t.Error("recommend mode should explain restrictions")
	}
}

func TestValidateActionsAgainstGuardrails_DestructiveBlocked(t *testing.T) {
	registry := map[string]*skill.Action{
		"scale-zero": {ID: "scale-zero", Tier: "destructive-mutation", Tool: "kubectl.scale"},
	}
	guardrails := &corev1alpha1.GuardrailsSpec{
		Autonomy: corev1alpha1.AutonomySafe,
	}

	warnings := validateActionsAgainstGuardrails(registry, guardrails)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "destructive-mutation") && strings.Contains(w, "blocked") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected destructive-mutation warning, got: %v", warnings)
	}
}

func TestValidateActionsAgainstGuardrails_DenyListMatch(t *testing.T) {
	registry := map[string]*skill.Action{
		"delete-ns": {ID: "delete-ns", Tier: "destructive-mutation", Tool: "kubectl.delete", TargetPattern: "namespace/backstage"},
	}
	guardrails := &corev1alpha1.GuardrailsSpec{
		Autonomy:      corev1alpha1.AutonomyDestructive,
		DeniedActions: []string{"kubectl.delete namespace/*"},
	}

	warnings := validateActionsAgainstGuardrails(registry, guardrails)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "deny list") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected deny list warning, got: %v", warnings)
	}
}

func TestValidateActionsAgainstGuardrails_ReadAtObserveOK(t *testing.T) {
	registry := map[string]*skill.Action{
		"check": {ID: "check", Tier: "read", Tool: "http.get"},
	}
	guardrails := &corev1alpha1.GuardrailsSpec{
		Autonomy: corev1alpha1.AutonomyObserve,
	}

	warnings := validateActionsAgainstGuardrails(registry, guardrails)
	if len(warnings) != 0 {
		t.Errorf("read actions at observe should produce no warnings, got: %v", warnings)
	}
}

func TestBuildPrompt_NoEndpoints(t *testing.T) {
	agent := testAgent()
	env := &resolver.ResolvedEnvironment{
		Name: "empty-env",
	}
	prompt := buildPrompt(agent, testSkills(), env, testModel())

	if !strings.Contains(prompt, "Environment: empty-env") {
		t.Error("prompt should contain environment name even with no endpoints")
	}
	if strings.Contains(prompt, "### Endpoints") {
		t.Error("should not have endpoints section when none defined")
	}
}

func TestBuildPrompt_NoDataResources(t *testing.T) {
	agent := testAgent()
	env := &resolver.ResolvedEnvironment{
		Name:      "no-data-env",
		Endpoints: map[string]corev1alpha1.EndpointSpec{"test": {URL: "https://test.com"}},
	}
	prompt := buildPrompt(agent, testSkills(), env, testModel())

	if strings.Contains(prompt, "Data Resources") {
		t.Error("should not have data resources section when none declared")
	}
}

func TestBuildPrompt_MultipleSkills(t *testing.T) {
	agent := testAgent()
	skills := []*skill.Skill{
		{Name: "skill-a", Instructions: "## Skill A\nDo thing A."},
		{Name: "skill-b", Instructions: "## Skill B\nDo thing B."},
	}
	prompt := buildPrompt(agent, skills, testEnvironment(), testModel())

	if !strings.Contains(prompt, "## Skill A") {
		t.Error("prompt should contain skill A")
	}
	if !strings.Contains(prompt, "## Skill B") {
		t.Error("prompt should contain skill B")
	}
	// A should come before B
	idxA := strings.Index(prompt, "## Skill A")
	idxB := strings.Index(prompt, "## Skill B")
	if idxA >= idxB {
		t.Error("skills should appear in order")
	}
}
