/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package engine

import (
	"fmt"
	"strings"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
	"github.com/marcus-qen/infraagent/internal/resolver"
)

// Data protection rules are HARDCODED. No configuration can disable them.
// This is a deliberate design decision: data is sacred.
//
// Blocked unconditionally:
//   - Delete PersistentVolumeClaim
//   - Delete PersistentVolume
//   - Modify PV reclaimPolicy (to Delete)
//   - Delete Namespace (cascades to all data within)
//   - Delete database CRDs (Cluster, Pooler, ScheduledBackup, etc.)
//   - Delete S3 objects/buckets
//   - DROP DATABASE / TRUNCATE / DELETE FROM (SQL operations)
//
// These rules exist because an LLM must NEVER be trusted with data deletion.
// Not at any autonomy level. Not with any override. Not ever.

// protectedResourceKinds lists Kubernetes resource kinds that are unconditionally
// protected from deletion. Matches are case-insensitive.
var protectedResourceKinds = map[string]string{
	"persistentvolumeclaim":     "PVC deletion destroys data",
	"persistentvolume":          "PV deletion destroys data",
	"pvc":                       "PVC deletion destroys data",
	"pv":                        "PV deletion destroys data",
	"namespace":                 "namespace deletion cascades to all contained data",
	"ns":                        "namespace deletion cascades to all contained data",
	// CNPG
	"cluster":                   "database cluster deletion destroys data",
	"pooler":                    "database pooler deletion disrupts data access",
	"scheduledbackup":           "scheduled backup deletion removes data protection",
	"backup":                    "backup deletion removes recovery capability",
	// Generic database operators
	"postgrescluster":           "database cluster deletion destroys data",
	"mysqlcluster":              "database cluster deletion destroys data",
	"redisfailover":             "database deletion destroys data",
	"cassandradatacenter":       "database datacenter deletion destroys data",
	"elasticsearchcluster":      "search cluster deletion destroys data",
	"kafkacluster":              "message broker deletion destroys data",
	"mongodbcommunity":          "database deletion destroys data",
}

// protectedToolPatterns lists tool+target patterns that are unconditionally blocked.
var protectedToolPatterns = []struct {
	toolPattern   string
	targetPattern string
	reason        string
}{
	// kubectl delete on protected resources
	{"kubectl.delete", "pvc*", "PVC deletion destroys data"},
	{"kubectl.delete", "pv*", "PV deletion destroys data"},
	{"kubectl.delete", "persistentvolumeclaim*", "PVC deletion destroys data"},
	{"kubectl.delete", "persistentvolume*", "PV deletion destroys data"},
	{"kubectl.delete", "namespace*", "namespace deletion cascades to all data"},
	{"kubectl.delete", "ns*", "namespace deletion cascades to all data"},
	{"kubectl.delete", "cluster*", "database cluster deletion destroys data"},

	// kubectl patch on PV reclaimPolicy
	{"kubectl.patch", "*reclaimPolicy*Delete*", "modifying PV reclaim to Delete risks data loss"},

	// S3/object storage operations
	{"http.delete", "*s3*", "S3 object deletion destroys data"},
	{"http.delete", "*minio*", "object storage deletion destroys data"},
	{"mcp.*.delete", "*", "MCP delete operations on data stores blocked"},

	// SQL operations (if we ever have a SQL tool)
	{"sql.*", "*drop*", "DROP operations destroy data"},
	{"sql.*", "*truncate*", "TRUNCATE operations destroy data"},
	{"sql.execute", "*delete from*", "bulk DELETE operations destroy data"},
}

// checkDataProtection applies hardcoded data protection rules.
// These rules cannot be configured, overridden, or disabled.
// Returns (blocked, reason).
func checkDataProtection(toolName, target string) (bool, string) {
	lower := strings.ToLower(toolName + " " + target)

	// Check for deletion of protected resource kinds
	if isDeleteTool(toolName) {
		kind := extractResourceKind(target)
		if reason, ok := protectedResourceKinds[strings.ToLower(kind)]; ok {
			return true, fmt.Sprintf("DATA PROTECTION: %s (attempted: %s %s)", reason, toolName, target)
		}
	}

	// Check protected patterns
	for _, pp := range protectedToolPatterns {
		if matchGlob(strings.ToLower(pp.toolPattern), strings.ToLower(toolName)) {
			if matchGlob(strings.ToLower(pp.targetPattern), strings.ToLower(target)) {
				return true, fmt.Sprintf("DATA PROTECTION: %s (attempted: %s %s)", pp.reason, toolName, target)
			}
		}
	}

	// Check for SQL-like destructive operations in any tool target
	sqlDestructive := []string{"drop database", "drop table", "truncate ", "delete from "}
	for _, pattern := range sqlDestructive {
		if strings.Contains(lower, pattern) {
			return true, fmt.Sprintf("DATA PROTECTION: SQL destructive operation detected (%s)", pattern)
		}
	}

	return false, ""
}

// isDeleteTool returns true if the tool name indicates a delete operation.
func isDeleteTool(toolName string) bool {
	lower := strings.ToLower(toolName)
	return strings.Contains(lower, "delete") || strings.Contains(lower, "remove") ||
		strings.Contains(lower, "destroy") || strings.Contains(lower, "purge")
}

// extractResourceKind extracts the Kubernetes resource kind from a target string.
// e.g. "pods -n backstage" → "pods", "pvc/my-data" → "pvc"
func extractResourceKind(target string) string {
	target = strings.TrimSpace(target)
	// Handle "kind/name" format
	if idx := strings.Index(target, "/"); idx > 0 {
		return target[:idx]
	}
	// Handle "kind name" or "kind -n namespace" format
	parts := strings.Fields(target)
	if len(parts) > 0 {
		return parts[0]
	}
	return target
}

// checkDataResourceImpact checks if a mutation targets or cascades to
// declared data resources in the environment.
func checkDataResourceImpact(
	toolName, target string,
	tier corev1alpha1.ActionTier,
	dataIndex *resolver.DataResourceIndex,
) (blocked bool, reason string) {
	// Only check mutations
	if tier == corev1alpha1.ActionTierRead {
		return false, ""
	}

	// Check if target namespace contains data resources
	ns := extractNamespace(target)
	if ns != "" && dataIndex.HasDataInNamespace(ns) {
		// Destructive mutations in data namespaces are blocked
		if tier == corev1alpha1.ActionTierDestructiveMutation {
			return true, fmt.Sprintf(
				"DATA RESOURCE IMPACT: destructive mutation in namespace %q which contains declared data resources",
				ns)
		}
	}

	// Check if target is directly a data resource
	kind := extractResourceKind(target)
	name := extractResourceName(target)
	if kind != "" && name != "" && ns != "" {
		if dataIndex.IsDataResource(kind, ns, name) {
			return true, fmt.Sprintf(
				"DATA RESOURCE IMPACT: action targets declared data resource %s/%s/%s",
				kind, ns, name)
		}
	}

	return false, ""
}

// extractNamespace extracts the namespace from a target string.
// Looks for -n/--namespace flags or namespace/name format.
func extractNamespace(target string) string {
	parts := strings.Fields(target)
	for i, part := range parts {
		if (part == "-n" || part == "--namespace") && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	// Check for format "type -n ns name" or detect from context
	return ""
}

// extractResourceName extracts the resource name from a target string.
func extractResourceName(target string) string {
	target = strings.TrimSpace(target)

	// Handle "kind/name" format
	if idx := strings.Index(target, "/"); idx > 0 {
		rest := target[idx+1:]
		// Handle "kind/name -n ns" format
		parts := strings.Fields(rest)
		if len(parts) > 0 {
			return parts[0]
		}
		return rest
	}

	// Handle "kind name" format (name is second word, excluding flags)
	parts := strings.Fields(target)
	for i := 1; i < len(parts); i++ {
		if parts[i] == "-n" || parts[i] == "--namespace" {
			i++ // skip namespace value
			continue
		}
		if strings.HasPrefix(parts[i], "-") {
			continue
		}
		return parts[i]
	}

	return ""
}
