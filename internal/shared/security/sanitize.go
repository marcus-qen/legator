/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package security provides credential hygiene and sanitization utilities
// for the LegatorAgent controller. It ensures that secrets, tokens, and
// other sensitive data never appear in LegatorRun status fields, prompts,
// or log output.
package security

import (
	"regexp"
	"strings"
)

// redactedPlaceholder replaces sensitive values.
const redactedPlaceholder = "[REDACTED]"

// Common patterns for secrets/tokens in tool output and LLM responses.
var sensitivePatterns = []*regexp.Regexp{
	// Bearer tokens
	regexp.MustCompile(`(?i)(bearer\s+)[a-zA-Z0-9\-_.~+/]+=*`),
	// Authorization headers
	regexp.MustCompile(`(?i)(authorization:\s*)(bearer\s+)?[a-zA-Z0-9\-_.~+/]+=*`),
	// Base64-encoded tokens (long sequences)
	regexp.MustCompile(`(?i)(token["\s:=]+)[a-zA-Z0-9+/]{40,}=*`),
	// Kubernetes service account tokens (JWTs)
	regexp.MustCompile(`eyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),
	// Generic API keys
	regexp.MustCompile(`(?i)(api[_-]?key["\s:=]+)[a-zA-Z0-9\-_.]{20,}`),
	// Vault tokens
	regexp.MustCompile(`hvs\.[a-zA-Z0-9]{20,}`),
	// AWS-style keys
	regexp.MustCompile(`(?i)(aws_secret_access_key["\s:=]+)[a-zA-Z0-9/+=]{20,}`),
	regexp.MustCompile(`AKIA[A-Z0-9]{16}`),
	// Password fields
	regexp.MustCompile(`(?i)(password["\s:=]+)\S+`),
	// Private key blocks
	regexp.MustCompile(`(?s)-----BEGIN[A-Z ]*PRIVATE KEY-----.*?-----END[A-Z ]*PRIVATE KEY-----`),
	// kubeconfig client-certificate-data / client-key-data
	regexp.MustCompile(`(?i)(client-(?:certificate|key)-data:\s*)[a-zA-Z0-9+/=\n]{40,}`),
}

// Sanitize scrubs sensitive data from text. It matches common secret
// patterns and replaces values with [REDACTED], preserving the prefix
// label where possible for readability.
func Sanitize(text string) string {
	result := text
	for _, pattern := range sensitivePatterns {
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			// Try to preserve the prefix (e.g. "token: " or "Authorization: ")
			loc := pattern.FindStringSubmatchIndex(match)
			if len(loc) >= 4 && loc[2] >= 0 {
				prefix := match[loc[2]:loc[3]]
				return prefix + redactedPlaceholder
			}
			return redactedPlaceholder
		})
	}
	return result
}

// ContainsSecret checks if text likely contains sensitive data.
func ContainsSecret(text string) bool {
	for _, pattern := range sensitivePatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// SanitizeActionResult sanitizes a tool call result before recording it
// in the LegatorRun audit trail. This truncates to maxLen after sanitizing.
func SanitizeActionResult(result string, maxLen int) string {
	sanitized := Sanitize(result)
	if maxLen > 0 && len(sanitized) > maxLen {
		return sanitized[:maxLen] + "... (truncated)"
	}
	return sanitized
}

// SanitizeMap sanitizes all values in a string map (e.g. resolved credentials
// before inclusion in prompts — should never happen, but defence in depth).
func SanitizeMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if isCredentialKey(k) {
			out[k] = redactedPlaceholder
		} else {
			out[k] = Sanitize(v)
		}
	}
	return out
}

// isCredentialKey checks if a map key name suggests it holds a secret.
func isCredentialKey(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range []string{"password", "secret", "token", "api_key", "apikey", "private_key", "credential"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}
