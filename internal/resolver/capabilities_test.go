/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package resolver

import (
	"testing"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

func TestValidateCapabilities_AllSatisfied(t *testing.T) {
	caps := &corev1alpha1.CapabilitiesSpec{
		Required: []string{"http", "kubectl.read"},
	}
	env := &ResolvedEnvironment{
		Endpoints: map[string]corev1alpha1.EndpointSpec{
			"test": {URL: "https://example.com"},
		},
	}

	result := ValidateCapabilities(caps, env)
	if !result.Satisfied {
		t.Errorf("expected satisfied, missing: %v", result.Missing)
	}
}

func TestValidateCapabilities_MissingRequired(t *testing.T) {
	caps := &corev1alpha1.CapabilitiesSpec{
		Required: []string{"http", "prometheus.query"},
	}
	env := &ResolvedEnvironment{
		Endpoints: map[string]corev1alpha1.EndpointSpec{
			"test": {URL: "https://example.com"},
		},
		// No MCP server providing prometheus.query
	}

	result := ValidateCapabilities(caps, env)
	if result.Satisfied {
		t.Error("expected unsatisfied — prometheus.query should be missing")
	}
	if len(result.Missing) != 1 || result.Missing[0] != "prometheus.query" {
		t.Errorf("Missing = %v, want [prometheus.query]", result.Missing)
	}
}

func TestValidateCapabilities_MCPProvides(t *testing.T) {
	caps := &corev1alpha1.CapabilitiesSpec{
		Required: []string{"k8sgpt.analyze"},
	}
	env := &ResolvedEnvironment{
		MCPServers: map[string]corev1alpha1.MCPServerSpec{
			"k8sgpt": {
				Endpoint:     "http://k8sgpt:8089",
				Capabilities: []string{"k8sgpt.analyze", "k8sgpt.filter"},
			},
		},
	}

	result := ValidateCapabilities(caps, env)
	if !result.Satisfied {
		t.Errorf("expected satisfied, missing: %v", result.Missing)
	}
}

func TestValidateCapabilities_NilCaps(t *testing.T) {
	env := &ResolvedEnvironment{}
	result := ValidateCapabilities(nil, env)
	if !result.Satisfied {
		t.Error("nil capabilities should be satisfied")
	}
}

func TestValidateCapabilities_OptionalMissing(t *testing.T) {
	caps := &corev1alpha1.CapabilitiesSpec{
		Required: []string{"http"},
		Optional: []string{"prometheus.query"},
	}
	env := &ResolvedEnvironment{
		Endpoints: map[string]corev1alpha1.EndpointSpec{
			"test": {URL: "https://example.com"},
		},
	}

	result := ValidateCapabilities(caps, env)
	if !result.Satisfied {
		t.Error("expected satisfied — optional shouldn't block")
	}
	if len(result.OptionalMissing) != 1 {
		t.Errorf("OptionalMissing = %v, want [prometheus.query]", result.OptionalMissing)
	}
}
