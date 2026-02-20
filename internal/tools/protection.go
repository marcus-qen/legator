/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"path/filepath"
	"strings"
)

// ProtectionEngine evaluates protection rules against tool actions.
type ProtectionEngine struct {
	classes []ProtectionClass
}

// NewProtectionEngine creates a protection engine with the given classes.
// Built-in classes (kubernetes-data, ssh-safety) are always included and
// cannot be weakened by user-provided classes.
func NewProtectionEngine(userClasses ...ProtectionClass) *ProtectionEngine {
	// Built-in classes always present
	classes := []ProtectionClass{
		DefaultKubernetesProtectionClass(),
		DefaultSSHProtectionClass(),
	}
	// User classes extend (never weaken) the built-ins
	classes = append(classes, userClasses...)
	return &ProtectionEngine{classes: classes}
}

// ProtectionResult is the outcome of evaluating an action against protection rules.
type ProtectionResult struct {
	// Allowed is true if no blocking rule matched.
	Allowed bool

	// MatchedRule is the rule that triggered (nil if Allowed).
	MatchedRule *ProtectionRule

	// MatchedClass is the protection class the rule belongs to.
	MatchedClass string

	// Action is the enforcement action (block/approve/audit).
	Action ProtectionAction
}

// Evaluate checks an action against all protection rules.
// domain: the tool domain (e.g. "kubernetes", "ssh")
// actionTarget: the full action string (e.g. "delete persistentvolumeclaim/my-pvc", "rm -rf /var/log")
func (pe *ProtectionEngine) Evaluate(domain, actionTarget string) ProtectionResult {
	for _, class := range pe.classes {
		for i := range class.Rules {
			rule := &class.Rules[i]

			// Domain must match (empty rule domain matches all)
			if rule.Domain != "" && !strings.EqualFold(rule.Domain, domain) {
				continue
			}

			// Pattern matching
			if matchPattern(rule.Pattern, actionTarget) {
				return ProtectionResult{
					Allowed:      rule.Action == ProtectionAudit,
					MatchedRule:  rule,
					MatchedClass: class.Name,
					Action:       rule.Action,
				}
			}
		}
	}

	// No rule matched — action is allowed
	return ProtectionResult{Allowed: true}
}

// matchPattern checks if the target matches the glob-style pattern.
// Supports:
//   - "*" matches any sequence of characters
//   - "?" matches any single character
//   - Patterns are case-insensitive
func matchPattern(pattern, target string) bool {
	pattern = strings.ToLower(pattern)
	target = strings.ToLower(target)

	// Use filepath.Match for single-segment patterns
	// For multi-segment patterns with *, do substring matching
	if strings.Contains(pattern, "/") || !strings.Contains(pattern, " ") {
		matched, _ := filepath.Match(pattern, target)
		if matched {
			return true
		}
	}

	// Also check if the target contains the pattern as a substring match
	// This handles patterns like "*dd if=*" matching "sudo dd if=/dev/zero"
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		inner := strings.Trim(pattern, "*")
		return strings.Contains(target, inner)
	}

	if strings.HasPrefix(pattern, "*") {
		suffix := strings.TrimPrefix(pattern, "*")
		// Handle patterns like "*/etc/shadow" — target should end with or contain the suffix
		if strings.HasSuffix(target, suffix) || strings.Contains(target, suffix) {
			return true
		}
	}

	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}

	// Word-based matching for command patterns like "delete persistentvolumeclaim/*"
	// Split pattern by spaces and check if all words match in order
	patternParts := strings.Fields(pattern)
	if len(patternParts) > 1 {
		targetLower := target
		for _, part := range patternParts {
			matched, _ := filepath.Match(part, extractMatchSegment(targetLower, part))
			if !matched && !strings.Contains(targetLower, strings.Trim(part, "*")) {
				return false
			}
		}
		return true
	}

	return false
}

// extractMatchSegment finds the segment in target that should be matched against the pattern part.
func extractMatchSegment(target, pattern string) string {
	fields := strings.Fields(target)
	patternClean := strings.Trim(pattern, "*")
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), patternClean) {
			return f
		}
		matched, _ := filepath.Match(pattern, f)
		if matched {
			return f
		}
	}
	if len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return target
}

// Classes returns the list of active protection classes.
func (pe *ProtectionEngine) Classes() []ProtectionClass {
	return pe.classes
}
