/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"testing"
)

func TestProtectionEngineBuiltins(t *testing.T) {
	pe := NewProtectionEngine()

	// Built-in K8s rules
	tests := []struct {
		name     string
		domain   string
		target   string
		allowed  bool
		wantRule string
	}{
		{"PVC delete blocked", "kubernetes", "delete persistentvolumeclaim/my-data", false, "kubernetes-data"},
		{"PV delete blocked", "kubernetes", "delete persistentvolume/pv-001", false, "kubernetes-data"},
		{"Namespace delete blocked", "kubernetes", "delete namespace/production", false, "kubernetes-data"},
		{"CNPG cluster delete blocked", "kubernetes", "delete clusters.postgresql.cnpg.io/backstage-db", false, "kubernetes-data"},
		{"Pod delete allowed", "kubernetes", "delete pod/nginx-abc123", true, ""},
		{"Get pods allowed", "kubernetes", "get pods -n monitoring", true, ""},

		// Built-in SSH rules
		{"Shadow file blocked", "ssh", "cat /etc/shadow", false, "ssh-safety"},
		{"dd blocked", "ssh", "sudo dd if=/dev/zero of=/dev/sda", false, "ssh-safety"},
		{"mkfs blocked", "ssh", "mkfs.ext4 /dev/sdb1", false, "ssh-safety"},
		{"rm -rf / blocked", "ssh", "rm -rf /", false, "ssh-safety"},
		{"ls allowed", "ssh", "ls -la /var/log", true, ""},
		{"cat log allowed", "ssh", "cat /var/log/syslog", true, ""},
		{"ps allowed", "ssh", "ps aux", true, ""},

		// Cross-domain (SSH rules don't apply to kubernetes)
		{"K8s action not matched by SSH rules", "kubernetes", "cat /etc/shadow", true, ""},
		// Domain-specific isolation
		{"SSH action not matched by K8s rules", "ssh", "delete persistentvolumeclaim/x", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pe.Evaluate(tt.domain, tt.target)
			if result.Allowed != tt.allowed {
				rule := ""
				if result.MatchedRule != nil {
					rule = result.MatchedRule.Pattern
				}
				t.Errorf("Evaluate(%q, %q) Allowed=%v, want %v (matched: %q class: %q)",
					tt.domain, tt.target, result.Allowed, tt.allowed, rule, result.MatchedClass)
			}
			if !tt.allowed && tt.wantRule != "" {
				if result.MatchedClass != tt.wantRule {
					t.Errorf("MatchedClass = %q, want %q", result.MatchedClass, tt.wantRule)
				}
			}
		})
	}
}

func TestProtectionEngineUserClasses(t *testing.T) {
	// Add custom protection class for production databases
	prodDB := ProtectionClass{
		Name:        "production-databases",
		Description: "Protects production database operations",
		Rules: []ProtectionRule{
			{Domain: "sql", Pattern: "*DROP TABLE*", Action: ProtectionBlock, Description: "Never drop tables"},
			{Domain: "sql", Pattern: "*TRUNCATE*", Action: ProtectionBlock, Description: "Never truncate tables"},
			{Domain: "sql", Pattern: "*DELETE FROM*", Action: ProtectionApprove, Description: "Bulk deletes need approval"},
			{Domain: "sql", Pattern: "*SELECT*", Action: ProtectionAudit, Description: "Audit all queries"},
		},
	}

	pe := NewProtectionEngine(prodDB)

	tests := []struct {
		name    string
		domain  string
		target  string
		allowed bool
		action  ProtectionAction
	}{
		{"DROP TABLE blocked", "sql", "DROP TABLE users", false, ProtectionBlock},
		{"TRUNCATE blocked", "sql", "TRUNCATE TABLE sessions", false, ProtectionBlock},
		{"DELETE needs approval", "sql", "DELETE FROM logs WHERE age > 30", false, ProtectionApprove},
		{"SELECT audited (but allowed)", "sql", "SELECT * FROM users", true, ProtectionAudit},
		{"SQL INSERT allowed (no matching rule)", "sql", "INSERT INTO logs VALUES (1, 'test')", true, ProtectionAction(0)},

		// Built-in classes still work
		{"K8s PVC still blocked", "kubernetes", "delete persistentvolumeclaim/data", false, ProtectionBlock},
		{"SSH shadow still blocked", "ssh", "cat /etc/shadow", false, ProtectionBlock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pe.Evaluate(tt.domain, tt.target)
			if result.Allowed != tt.allowed {
				t.Errorf("Evaluate(%q, %q) Allowed=%v, want %v", tt.domain, tt.target, result.Allowed, tt.allowed)
			}
			if !tt.allowed {
				if result.Action != tt.action {
					t.Errorf("Action = %v, want %v", result.Action, tt.action)
				}
			}
		})
	}
}

func TestProtectionEngineEmptyTarget(t *testing.T) {
	pe := NewProtectionEngine()

	// Empty target should be allowed (no rule matches)
	result := pe.Evaluate("kubernetes", "")
	if !result.Allowed {
		t.Error("Empty target should be allowed")
	}

	// Unknown domain should be allowed (no rules match)
	result = pe.Evaluate("unknown-domain", "some action")
	if !result.Allowed {
		t.Error("Unknown domain should be allowed")
	}
}

func TestProtectionEngineClassList(t *testing.T) {
	custom := ProtectionClass{
		Name:        "custom",
		Description: "Custom rules",
		Rules:       []ProtectionRule{},
	}

	pe := NewProtectionEngine(custom)
	classes := pe.Classes()

	// Should have built-in K8s + SSH + custom
	if len(classes) != 3 {
		t.Errorf("Expected 3 classes, got %d", len(classes))
	}

	names := make(map[string]bool)
	for _, c := range classes {
		names[c.Name] = true
	}

	for _, want := range []string{"kubernetes-data", "ssh-safety", "custom"} {
		if !names[want] {
			t.Errorf("Missing protection class: %q", want)
		}
	}
}
