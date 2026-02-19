/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcp

import (
	"strings"
)

// K8sGPTNoiseFilter removes known false positives from k8sgpt analysis results.
//
// k8sgpt's analyzers surface a lot of noise that is not actionable:
//   - kube-root-ca.crt ConfigMaps exist in every namespace (not unused)
//   - Kyverno policy violation events show up as Service "errors"
//   - CNPG read-only services with no replicas are expected (no replicas configured)
//
// This filter strips those items from the output so agents see only real issues.
func K8sGPTNoiseFilter(serverName, toolName, result string) string {
	if serverName != "k8sgpt" {
		return result
	}

	lines := strings.Split(result, "\n")
	var filtered []string

	for _, line := range lines {
		if shouldFilterK8sGPTLine(line) {
			continue
		}
		filtered = append(filtered, line)
	}

	// If everything was filtered, return a summary instead of empty
	text := strings.TrimSpace(strings.Join(filtered, "\n"))
	if text == "" {
		return "(all results filtered — no actionable findings)"
	}

	return text
}

// shouldFilterK8sGPTLine returns true if a line should be removed.
func shouldFilterK8sGPTLine(line string) bool {
	lower := strings.ToLower(line)

	// kube-root-ca.crt is a standard ConfigMap in every namespace
	if strings.Contains(lower, "kube-root-ca.crt") {
		return true
	}

	// Kyverno policy violation events are not service errors
	if strings.Contains(lower, "kyverno") && strings.Contains(lower, "policy") {
		return true
	}

	// CNPG read-only services with no endpoints are expected when no replicas
	if strings.Contains(lower, "-ro") && strings.Contains(lower, "no endpoints") {
		return true
	}

	return false
}

// DefaultNoiseFilters returns the standard set of noise filters.
func DefaultNoiseFilters() []NoiseFilter {
	return []NoiseFilter{
		K8sGPTNoiseFilter,
	}
}
