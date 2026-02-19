/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package engine

import (
	"testing"
	"time"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
	"github.com/marcus-qen/infraagent/internal/resolver"
	"github.com/marcus-qen/infraagent/internal/skill"
)

// --- Matcher tests (Step 2.6) ---

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		text    string
		want    bool
	}{
		// Exact match
		{"kubectl.get", "kubectl.get", true},
		{"kubectl.get", "kubectl.delete", false},
		// Wildcard suffix
		{"kubectl.*", "kubectl.get", true},
		{"kubectl.*", "kubectl.delete", true},
		{"kubectl.*", "http.get", false},
		// Wildcard prefix
		{"*.delete", "kubectl.delete", true},
		{"*.delete", "http.delete", true},
		{"*.delete", "kubectl.get", false},
		// Wildcard middle
		{"kubectl.*.pods", "kubectl.get.pods", true},
		{"kubectl.*.pods", "kubectl.delete.pods", true},
		{"kubectl.*.pods", "kubectl.get.services", false},
		// Double wildcard
		{"*delete*", "kubectl.delete.pods", true},
		{"*delete*", "http.delete", true},
		{"*delete*", "kubectl.get", false},
		// Namespace glob
		{"pods -n backstage*", "pods -n backstage", true},
		{"pods -n backstage*", "pods -n backstage-dev", true},
		{"pods -n backstage*", "pods -n monitoring", false},
		// No wildcard, no match
		{"exact", "exact", true},
		{"exact", "not-exact", false},
	}

	for _, tt := range tests {
		got := matchGlob(tt.pattern, tt.text)
		if got != tt.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.text, got, tt.want)
		}
	}
}

func TestMatchToolAction(t *testing.T) {
	action := &skill.Action{
		ID:            "check-pods",
		Tool:          "kubectl.get",
		TargetPattern: "pods -n *",
		Tier:          "read",
	}

	tests := []struct {
		tool   string
		target string
		want   bool
	}{
		{"kubectl.get", "pods -n backstage", true},
		{"kubectl.get", "pods -n monitoring", true},
		{"kubectl.get", "deployments -n backstage", false},
		{"kubectl.delete", "pods -n backstage", false},
	}

	for _, tt := range tests {
		got := matchToolAction(action, tt.tool, tt.target)
		if got != tt.want {
			t.Errorf("matchToolAction(%q, %q) = %v, want %v", tt.tool, tt.target, got, tt.want)
		}
	}
}

// --- Classifier tests (Step 2.7) ---

func TestClassifyTier(t *testing.T) {
	tests := []struct {
		tier string
		want corev1alpha1.ActionTier
	}{
		{"read", corev1alpha1.ActionTierRead},
		{"service-mutation", corev1alpha1.ActionTierServiceMutation},
		{"destructive-mutation", corev1alpha1.ActionTierDestructiveMutation},
		{"data-mutation", corev1alpha1.ActionTierDataMutation},
		{"unknown", corev1alpha1.ActionTierDestructiveMutation}, // conservative default
		{"", corev1alpha1.ActionTierDestructiveMutation},
	}

	for _, tt := range tests {
		got := classifyTier(tt.tier)
		if got != tt.want {
			t.Errorf("classifyTier(%q) = %v, want %v", tt.tier, got, tt.want)
		}
	}
}

func TestClassifyFromToolName(t *testing.T) {
	tests := []struct {
		tool string
		want corev1alpha1.ActionTier
	}{
		{"kubectl.get", corev1alpha1.ActionTierRead},
		{"kubectl.list", corev1alpha1.ActionTierRead},
		{"kubectl.describe", corev1alpha1.ActionTierRead},
		{"kubectl.logs", corev1alpha1.ActionTierRead},
		{"http.get", corev1alpha1.ActionTierRead},
		{"kubectl.delete", corev1alpha1.ActionTierDestructiveMutation},
		{"kubectl.destroy", corev1alpha1.ActionTierDestructiveMutation},
		{"kubectl.apply", corev1alpha1.ActionTierServiceMutation},
		{"kubectl.rollout", corev1alpha1.ActionTierServiceMutation},
		{"http.post", corev1alpha1.ActionTierServiceMutation},
	}

	for _, tt := range tests {
		got := classifyFromToolName(tt.tool)
		if got != tt.want {
			t.Errorf("classifyFromToolName(%q) = %v, want %v", tt.tool, got, tt.want)
		}
	}
}

// --- Autonomy Enforcer tests (Step 2.8) ---

func TestCheckAutonomy(t *testing.T) {
	tests := []struct {
		tier     corev1alpha1.ActionTier
		autonomy corev1alpha1.AutonomyLevel
		blocked  bool
	}{
		// Read actions always allowed
		{corev1alpha1.ActionTierRead, corev1alpha1.AutonomyObserve, false},
		{corev1alpha1.ActionTierRead, corev1alpha1.AutonomyRecommend, false},
		{corev1alpha1.ActionTierRead, corev1alpha1.AutonomySafe, false},
		{corev1alpha1.ActionTierRead, corev1alpha1.AutonomyDestructive, false},

		// Service mutations need automate-safe or above
		{corev1alpha1.ActionTierServiceMutation, corev1alpha1.AutonomyObserve, true},
		{corev1alpha1.ActionTierServiceMutation, corev1alpha1.AutonomyRecommend, true},
		{corev1alpha1.ActionTierServiceMutation, corev1alpha1.AutonomySafe, false},
		{corev1alpha1.ActionTierServiceMutation, corev1alpha1.AutonomyDestructive, false},

		// Destructive mutations need automate-destructive
		{corev1alpha1.ActionTierDestructiveMutation, corev1alpha1.AutonomyObserve, true},
		{corev1alpha1.ActionTierDestructiveMutation, corev1alpha1.AutonomyRecommend, true},
		{corev1alpha1.ActionTierDestructiveMutation, corev1alpha1.AutonomySafe, true},
		{corev1alpha1.ActionTierDestructiveMutation, corev1alpha1.AutonomyDestructive, false},

		// Data mutations ALWAYS blocked — no autonomy level unlocks them
		{corev1alpha1.ActionTierDataMutation, corev1alpha1.AutonomyObserve, true},
		{corev1alpha1.ActionTierDataMutation, corev1alpha1.AutonomyRecommend, true},
		{corev1alpha1.ActionTierDataMutation, corev1alpha1.AutonomySafe, true},
		{corev1alpha1.ActionTierDataMutation, corev1alpha1.AutonomyDestructive, true},
	}

	for _, tt := range tests {
		blocked, _ := checkAutonomy(tt.tier, tt.autonomy)
		if blocked != tt.blocked {
			t.Errorf("checkAutonomy(tier=%s, autonomy=%s) blocked=%v, want=%v",
				tt.tier, tt.autonomy, blocked, tt.blocked)
		}
	}
}

// --- Allow/Deny List tests (Step 2.9) ---

func TestCheckDenyList(t *testing.T) {
	deniedActions := []string{
		"kubectl.delete namespace*",
		"kubectl.delete pvc*",
		"*drop*",
	}

	tests := []struct {
		tool    string
		target  string
		blocked bool
	}{
		{"kubectl.delete", "namespace backstage", true},
		{"kubectl.delete", "pvc/my-data", true},
		{"sql.execute", "drop table users", true},
		{"kubectl.get", "pods", false},
		{"kubectl.delete", "pods my-pod", false},
	}

	for _, tt := range tests {
		blocked, _ := checkDenyList(tt.tool, tt.target, deniedActions)
		if blocked != tt.blocked {
			t.Errorf("checkDenyList(%q, %q) blocked=%v, want=%v",
				tt.tool, tt.target, blocked, tt.blocked)
		}
	}
}

func TestCheckAllowList(t *testing.T) {
	tests := []struct {
		name           string
		tool           string
		target         string
		allowedActions []string
		blocked        bool
	}{
		{
			name:           "empty allow list permits all",
			tool:           "kubectl.anything",
			target:         "whatever",
			allowedActions: nil,
			blocked:        false,
		},
		{
			name:           "matching allow list permits",
			tool:           "kubectl.get",
			target:         "pods",
			allowedActions: []string{"kubectl.get*", "kubectl.logs*"},
			blocked:        false,
		},
		{
			name:           "non-matching allow list blocks",
			tool:           "kubectl.delete",
			target:         "pods my-pod",
			allowedActions: []string{"kubectl.get*", "kubectl.logs*"},
			blocked:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, _ := checkAllowList(tt.tool, tt.target, tt.allowedActions)
			if blocked != tt.blocked {
				t.Errorf("checkAllowList blocked=%v, want=%v", blocked, tt.blocked)
			}
		})
	}
}

// --- Cooldown tests (Step 2.11) ---

func TestCooldownTracker(t *testing.T) {
	tracker := NewCooldownTracker()

	// Not in cooldown initially
	if tracker.Check("agent1", "restart", "deploy/backstage", 5*time.Minute) {
		t.Error("should not be in cooldown before any execution")
	}

	// Record execution
	tracker.Record("agent1", "restart", "deploy/backstage")

	// Now in cooldown
	if !tracker.Check("agent1", "restart", "deploy/backstage", 5*time.Minute) {
		t.Error("should be in cooldown after execution")
	}

	// Different agent not in cooldown
	if tracker.Check("agent2", "restart", "deploy/backstage", 5*time.Minute) {
		t.Error("different agent should not be in cooldown")
	}

	// Different target not in cooldown
	if tracker.Check("agent1", "restart", "deploy/frontend", 5*time.Minute) {
		t.Error("different target should not be in cooldown")
	}

	// Zero cooldown always passes
	if tracker.Check("agent1", "restart", "deploy/backstage", 0) {
		t.Error("zero cooldown should always pass")
	}
}

// --- Data Protection tests (Step 2.12) ---

func TestCheckDataProtection_PVC(t *testing.T) {
	tests := []struct {
		tool    string
		target  string
		blocked bool
	}{
		// PVC deletion — ALWAYS blocked
		{"kubectl.delete", "pvc/my-data -n backstage", true},
		{"kubectl.delete", "persistentvolumeclaim my-data", true},
		{"kubectl.delete", "pvc my-data -n production", true},

		// PV deletion — ALWAYS blocked
		{"kubectl.delete", "pv/data-volume", true},
		{"kubectl.delete", "persistentvolume data-volume", true},

		// Namespace deletion — ALWAYS blocked
		{"kubectl.delete", "namespace backstage", true},
		{"kubectl.delete", "ns production", true},

		// Database CR deletion — ALWAYS blocked
		{"kubectl.delete", "cluster backstage-db -n backstage", true},
		{"kubectl.delete", "scheduledbackup daily-backup -n backstage", true},
		{"kubectl.delete", "backup my-backup -n cnpg-system", true},

		// S3 operations — ALWAYS blocked
		{"http.delete", "https://s3.example.com/my-bucket/key", true},
		{"http.delete", "https://minio.local/backup-bucket/file", true},

		// SQL destructive — ALWAYS blocked
		{"sql.execute", "DROP DATABASE backstage", true},
		{"sql.execute", "TRUNCATE users", true},
		{"sql.execute", "DELETE FROM sessions", true},

		// Normal operations — NOT blocked
		{"kubectl.delete", "pods my-pod -n backstage", false},
		{"kubectl.delete", "deployment my-deploy -n backstage", false},
		{"kubectl.delete", "configmap my-config -n backstage", false},
		{"kubectl.get", "pvc/my-data", false},
		{"http.get", "https://s3.example.com/my-bucket/key", false},
	}

	for _, tt := range tests {
		blocked, reason := checkDataProtection(tt.tool, tt.target)
		if blocked != tt.blocked {
			t.Errorf("checkDataProtection(%q, %q) blocked=%v (reason: %s), want=%v",
				tt.tool, tt.target, blocked, reason, tt.blocked)
		}
	}
}

// Test that every hardcoded blocked operation is actually blocked.
// This is the CRITICAL safety test — zero false negatives allowed.
func TestCheckDataProtection_ComprehensiveBlocking(t *testing.T) {
	// Every single one of these MUST be blocked
	mustBlock := []struct {
		tool   string
		target string
		desc   string
	}{
		{"kubectl.delete", "pvc my-data", "PVC deletion"},
		{"kubectl.delete", "pv my-volume", "PV deletion"},
		{"kubectl.delete", "namespace production", "namespace deletion"},
		{"kubectl.delete", "ns staging", "namespace deletion (ns shorthand)"},
		{"kubectl.delete", "persistentvolumeclaim my-data", "PVC deletion (full name)"},
		{"kubectl.delete", "persistentvolume my-volume", "PV deletion (full name)"},
		{"kubectl.delete", "cluster backstage-db", "CNPG cluster deletion"},
		{"kubectl.delete", "scheduledbackup nightly", "scheduled backup deletion"},
		{"kubectl.delete", "backup my-backup", "backup deletion"},
		{"kubectl.remove", "pvc my-data", "PVC removal (alternative verb)"},
		{"kubectl.destroy", "namespace test", "namespace destruction"},
		{"sql.execute", "DROP DATABASE users", "SQL DROP DATABASE"},
		{"sql.execute", "drop table sessions", "SQL DROP TABLE (lowercase)"},
		{"sql.execute", "TRUNCATE orders", "SQL TRUNCATE"},
		{"sql.execute", "DELETE FROM users WHERE 1=1", "SQL DELETE FROM"},
		{"http.delete", "https://s3.us-east-1.amazonaws.com/bucket/key", "S3 delete"},
		{"http.delete", "https://minio.example.com/bucket/object", "MinIO delete"},
	}

	for _, tt := range mustBlock {
		blocked, reason := checkDataProtection(tt.tool, tt.target)
		if !blocked {
			t.Errorf("SAFETY FAILURE: %s not blocked! tool=%q target=%q reason=%q",
				tt.desc, tt.tool, tt.target, reason)
		}
	}
}

// Test that read operations are never blocked.
func TestCheckDataProtection_ReadsNeverBlocked(t *testing.T) {
	readOps := []struct {
		tool   string
		target string
	}{
		{"kubectl.get", "pvc/my-data"},
		{"kubectl.get", "pv/my-volume"},
		{"kubectl.get", "namespace/production"},
		{"kubectl.describe", "pvc my-data -n backstage"},
		{"kubectl.logs", "pod/my-pod -n backstage"},
		{"http.get", "https://s3.example.com/bucket/key"},
		{"sql.query", "SELECT * FROM users"},
	}

	for _, tt := range readOps {
		blocked, _ := checkDataProtection(tt.tool, tt.target)
		if blocked {
			t.Errorf("read operation blocked: tool=%q target=%q", tt.tool, tt.target)
		}
	}
}

// --- Data Resource Impact tests (Step 2.13) ---

func TestCheckDataResourceImpact(t *testing.T) {
	idx := &resolver.DataResourceIndex{}
	// Build a test index manually via the resolver package
	// For testing, we use a simulated index
	testIdx := buildTestDataIndex()

	tests := []struct {
		tool    string
		target  string
		tier    corev1alpha1.ActionTier
		blocked bool
	}{
		// Destructive mutation in namespace with data → blocked
		{"kubectl.delete", "deployment my-app -n backstage", corev1alpha1.ActionTierDestructiveMutation, true},
		// Service mutation in namespace with data → allowed (only destructive blocked)
		{"kubectl.rollout", "deployment my-app -n backstage", corev1alpha1.ActionTierServiceMutation, false},
		// Read in namespace with data → allowed
		{"kubectl.get", "pods -n backstage", corev1alpha1.ActionTierRead, false},
		// Destructive mutation in namespace without data → allowed
		{"kubectl.delete", "deployment my-app -n monitoring", corev1alpha1.ActionTierDestructiveMutation, false},
	}

	_ = idx // unused, using testIdx
	for _, tt := range tests {
		blocked, _ := checkDataResourceImpact(tt.tool, tt.target, tt.tier, testIdx)
		if blocked != tt.blocked {
			t.Errorf("checkDataResourceImpact(%q, %q, %s) blocked=%v, want=%v",
				tt.tool, tt.target, tt.tier, blocked, tt.blocked)
		}
	}
}

func buildTestDataIndex() *resolver.DataResourceIndex {
	// Use the exported builder via a dummy DataResourcesSpec
	spec := &corev1alpha1.DataResourcesSpec{
		Databases: []corev1alpha1.DataResourceRef{
			{Name: "backstage-db", Namespace: "backstage", Kind: "Cluster"},
		},
		PersistentStorage: []corev1alpha1.DataResourceRef{
			{Name: "grafana-data", Namespace: "monitoring-grafana", Kind: "PVC"},
		},
	}
	return resolver.BuildDataIndexFromSpec(spec)
}

// --- Full Engine Integration tests (Step 2.28) ---

func TestEngine_ReadAction_AlwaysAllowed(t *testing.T) {
	eng := NewEngine("test-agent", &corev1alpha1.GuardrailsSpec{
		Autonomy:      corev1alpha1.AutonomyObserve,
		MaxIterations: 10,
	}, map[string]*skill.Action{
		"check-pods": {
			ID:   "check-pods",
			Tool: "kubectl.get",
			Tier: "read",
		},
	}, nil)

	d := eng.Evaluate("kubectl.get", "pods -n backstage")
	if !d.Allowed {
		t.Errorf("read action should be allowed even with observe autonomy, got blocked: %s", d.BlockReason)
	}
}

func TestEngine_MutationBlocked_ObserveMode(t *testing.T) {
	eng := NewEngine("test-agent", &corev1alpha1.GuardrailsSpec{
		Autonomy:      corev1alpha1.AutonomyObserve,
		MaxIterations: 10,
	}, map[string]*skill.Action{
		"restart-deploy": {
			ID:   "restart-deploy",
			Tool: "kubectl.rollout",
			Tier: "service-mutation",
		},
	}, nil)

	d := eng.Evaluate("kubectl.rollout", "deployment backstage -n backstage")
	if d.Allowed {
		t.Error("mutation should be blocked in observe mode")
	}
	if d.PreFlight.AutonomyCheck != "BLOCKED" {
		t.Errorf("autonomy check should be BLOCKED, got %q", d.PreFlight.AutonomyCheck)
	}
}

func TestEngine_DataMutation_AlwaysBlocked(t *testing.T) {
	// Even with the highest autonomy level, data mutations are blocked
	eng := NewEngine("test-agent", &corev1alpha1.GuardrailsSpec{
		Autonomy:      corev1alpha1.AutonomyDestructive,
		MaxIterations: 10,
	}, nil, nil)

	d := eng.Evaluate("kubectl.delete", "pvc/my-data -n production")
	if d.Allowed {
		t.Fatal("PVC deletion MUST be blocked regardless of autonomy level — SAFETY FAILURE")
	}
	if d.PreFlight.DataProtection != "BLOCKED" {
		t.Errorf("data protection should be BLOCKED, got %q", d.PreFlight.DataProtection)
	}
}

func TestEngine_UndeclaredMutation_Blocked(t *testing.T) {
	eng := NewEngine("test-agent", &corev1alpha1.GuardrailsSpec{
		Autonomy:      corev1alpha1.AutonomySafe,
		MaxIterations: 10,
	}, map[string]*skill.Action{
		// Only declared action is a read
		"check-pods": {
			ID:   "check-pods",
			Tool: "kubectl.get",
			Tier: "read",
		},
	}, nil)

	// Try a mutation that's not in the Action Sheet
	d := eng.Evaluate("kubectl.rollout", "deployment backstage -n backstage")
	if d.Allowed {
		t.Error("undeclared mutation should be blocked (allowlist principle)")
	}
}

func TestEngine_DenyListOverridesAllowList(t *testing.T) {
	eng := NewEngine("test-agent", &corev1alpha1.GuardrailsSpec{
		Autonomy:       corev1alpha1.AutonomySafe,
		AllowedActions: []string{"kubectl.*"},
		DeniedActions:  []string{"kubectl.delete*"},
		MaxIterations:  10,
	}, map[string]*skill.Action{
		"delete-pod": {
			ID:   "delete-pod",
			Tool: "kubectl.delete",
			Tier: "service-mutation",
		},
	}, nil)

	d := eng.Evaluate("kubectl.delete", "pod my-pod -n backstage")
	if d.Allowed {
		t.Error("deny list should override allow list")
	}
}

// --- Boundary tests (Step 2.29) ---

func TestAutonomyRank(t *testing.T) {
	if autonomyRank(corev1alpha1.AutonomyObserve) >= autonomyRank(corev1alpha1.AutonomyRecommend) {
		t.Error("observe should rank below recommend")
	}
	if autonomyRank(corev1alpha1.AutonomyRecommend) >= autonomyRank(corev1alpha1.AutonomySafe) {
		t.Error("recommend should rank below safe")
	}
	if autonomyRank(corev1alpha1.AutonomySafe) >= autonomyRank(corev1alpha1.AutonomyDestructive) {
		t.Error("safe should rank below destructive")
	}
}
