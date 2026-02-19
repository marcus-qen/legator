/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package resolver

import (
	"fmt"
	"strings"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

// CapabilityCheckResult reports whether required capabilities are satisfied.
type CapabilityCheckResult struct {
	// Satisfied is true if all required capabilities are met.
	Satisfied bool

	// Missing lists required capabilities that are not available.
	Missing []string

	// Available lists all capabilities from the environment.
	Available []string

	// OptionalMissing lists optional capabilities not available.
	OptionalMissing []string
}

// ValidateCapabilities checks that the agent's required capabilities
// can be satisfied by the environment's MCP servers and connection type.
func ValidateCapabilities(
	caps *corev1alpha1.CapabilitiesSpec,
	env *ResolvedEnvironment,
) *CapabilityCheckResult {
	result := &CapabilityCheckResult{Satisfied: true}

	if caps == nil {
		return result
	}

	// Build the set of available capabilities
	available := make(map[string]bool)

	// Connection-derived capabilities
	if env.Endpoints != nil {
		available["http"] = true
	}

	// kubectl is available if we have a k8s connection
	// (in-cluster or kubeconfig)
	available["kubectl.read"] = true
	available["kubectl.write.safe"] = true

	// MCP server capabilities
	for _, mcp := range env.MCPServers {
		for _, cap := range mcp.Capabilities {
			available[cap] = true
		}
	}

	for k := range available {
		result.Available = append(result.Available, k)
	}

	// Check required capabilities
	for _, req := range caps.Required {
		if !matchCapability(req, available) {
			result.Satisfied = false
			result.Missing = append(result.Missing, req)
		}
	}

	// Check optional capabilities (informational)
	for _, opt := range caps.Optional {
		if !matchCapability(opt, available) {
			result.OptionalMissing = append(result.OptionalMissing, opt)
		}
	}

	return result
}

// matchCapability checks if a required capability is satisfied.
// Supports prefix matching: "kubectl" matches "kubectl.read".
func matchCapability(required string, available map[string]bool) bool {
	if available[required] {
		return true
	}
	// Prefix match: "kubectl" is satisfied by "kubectl.read"
	for cap := range available {
		if strings.HasPrefix(cap, required+".") || strings.HasPrefix(required, cap+".") {
			return true
		}
	}
	return false
}

// FormatCapabilityError returns a human-readable error for unsatisfied capabilities.
func FormatCapabilityError(result *CapabilityCheckResult) string {
	if result.Satisfied {
		return ""
	}
	return fmt.Sprintf("unsatisfied capabilities: %s (available: %s)",
		strings.Join(result.Missing, ", "),
		strings.Join(result.Available, ", "))
}
