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

func TestActionTierString(t *testing.T) {
	tests := []struct {
		tier ActionTier
		want string
	}{
		{TierRead, "read"},
		{TierServiceMutation, "service-mutation"},
		{TierDestructiveMutation, "destructive-mutation"},
		{TierDataMutation, "data-mutation"},
		{ActionTier(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("ActionTier(%d).String() = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

func TestParseActionTier(t *testing.T) {
	tests := []struct {
		input string
		want  ActionTier
	}{
		{"read", TierRead},
		{"service-mutation", TierServiceMutation},
		{"destructive-mutation", TierDestructiveMutation},
		{"data-mutation", TierDataMutation},
		{"unknown-value", TierDataMutation}, // Unknown defaults to most restrictive
		{"", TierDataMutation},
	}
	for _, tt := range tests {
		if got := ParseActionTier(tt.input); got != tt.want {
			t.Errorf("ParseActionTier(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestProtectionActionString(t *testing.T) {
	tests := []struct {
		action ProtectionAction
		want   string
	}{
		{ProtectionBlock, "block"},
		{ProtectionApprove, "approve"},
		{ProtectionAudit, "audit"},
		{ProtectionAction(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.action.String(); got != tt.want {
			t.Errorf("ProtectionAction(%d).String() = %q, want %q", tt.action, got, tt.want)
		}
	}
}

func TestDefaultKubernetesProtectionClass(t *testing.T) {
	pc := DefaultKubernetesProtectionClass()

	if pc.Name != "kubernetes-data" {
		t.Errorf("Name = %q, want 'kubernetes-data'", pc.Name)
	}

	if len(pc.Rules) < 5 {
		t.Errorf("Expected at least 5 rules, got %d", len(pc.Rules))
	}

	// All rules should be Block actions for the K8s default
	for _, rule := range pc.Rules {
		if rule.Action != ProtectionBlock {
			t.Errorf("Rule %q has action %v, expected Block", rule.Pattern, rule.Action)
		}
		if rule.Domain != "kubernetes" {
			t.Errorf("Rule %q has domain %q, expected 'kubernetes'", rule.Pattern, rule.Domain)
		}
	}
}

func TestDefaultSSHProtectionClass(t *testing.T) {
	pc := DefaultSSHProtectionClass()

	if pc.Name != "ssh-safety" {
		t.Errorf("Name = %q, want 'ssh-safety'", pc.Name)
	}

	if len(pc.Rules) < 5 {
		t.Errorf("Expected at least 5 rules, got %d", len(pc.Rules))
	}

	// All SSH rules should have domain "ssh"
	for _, rule := range pc.Rules {
		if rule.Domain != "ssh" {
			t.Errorf("Rule %q has domain %q, expected 'ssh'", rule.Pattern, rule.Domain)
		}
	}

	// Check that shadow file is blocked
	found := false
	for _, rule := range pc.Rules {
		if rule.Pattern == "*/etc/shadow" && rule.Action == ProtectionBlock {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected /etc/shadow to be blocked")
	}
}

func TestToolCapabilityDomain(t *testing.T) {
	cap := ToolCapability{
		Domain:              "ssh",
		SupportedTiers:      []ActionTier{TierRead, TierServiceMutation, TierDestructiveMutation},
		RequiresCredentials: true,
		RequiresConnection:  true,
	}

	if cap.Domain != "ssh" {
		t.Errorf("Domain = %q, want 'ssh'", cap.Domain)
	}
	if !cap.RequiresCredentials {
		t.Error("SSH tool should require credentials")
	}
	if !cap.RequiresConnection {
		t.Error("SSH tool should require connection")
	}
	if len(cap.SupportedTiers) != 3 {
		t.Errorf("Expected 3 supported tiers, got %d", len(cap.SupportedTiers))
	}
}

func TestActionClassificationBlocked(t *testing.T) {
	ac := ActionClassification{
		Tier:        TierDataMutation,
		Target:      "root@server:/etc/shadow",
		Description: "Attempt to read shadow file",
		Blocked:     true,
		BlockReason: "Protected file: /etc/shadow",
	}

	if !ac.Blocked {
		t.Error("Expected action to be blocked")
	}
	if ac.Tier != TierDataMutation {
		t.Errorf("Expected data-mutation tier, got %v", ac.Tier)
	}
}
