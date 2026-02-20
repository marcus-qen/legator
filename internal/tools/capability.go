/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

// ActionTier classifies the risk level of a tool action.
type ActionTier int

const (
	// TierRead is a read-only action with no side effects.
	TierRead ActionTier = iota
	// TierServiceMutation changes running services (restart, scale, deploy).
	TierServiceMutation
	// TierDestructiveMutation destroys or irreversibly modifies resources.
	TierDestructiveMutation
	// TierDataMutation modifies data (databases, files, object storage).
	// Always blocked by default — requires explicit approval.
	TierDataMutation
)

// String returns the human-readable name of an action tier.
func (t ActionTier) String() string {
	switch t {
	case TierRead:
		return "read"
	case TierServiceMutation:
		return "service-mutation"
	case TierDestructiveMutation:
		return "destructive-mutation"
	case TierDataMutation:
		return "data-mutation"
	default:
		return "unknown"
	}
}

// ParseActionTier converts a string to an ActionTier.
func ParseActionTier(s string) ActionTier {
	switch s {
	case "read":
		return TierRead
	case "service-mutation":
		return TierServiceMutation
	case "destructive-mutation":
		return TierDestructiveMutation
	case "data-mutation":
		return TierDataMutation
	default:
		return TierDataMutation // Unknown = most restrictive
	}
}

// ToolCapability declares what a tool can do.
type ToolCapability struct {
	// Domain is the tool's operational domain (e.g. "kubernetes", "ssh", "http", "sql", "dns").
	Domain string

	// SupportedTiers lists the action tiers this tool can perform.
	SupportedTiers []ActionTier

	// RequiresCredentials indicates whether the tool needs credential injection.
	RequiresCredentials bool

	// RequiresConnection indicates whether the tool needs an active connection (SSH, DB, etc.).
	RequiresConnection bool
}

// ActionClassification is the result of classifying a tool action.
type ActionClassification struct {
	// Tier is the risk level of this specific action.
	Tier ActionTier

	// Target describes what the action operates on (e.g. "pods -n monitoring", "root@server:/etc/nginx").
	Target string

	// Description is a human-readable summary of the action.
	Description string

	// Blocked indicates the action should be unconditionally blocked.
	Blocked bool

	// BlockReason explains why the action is blocked (if Blocked is true).
	BlockReason string
}

// ClassifiableTool extends Tool with action classification capabilities.
// Tools that implement this interface allow the guardrail engine to make
// fine-grained decisions about individual actions, not just tool-level checks.
type ClassifiableTool interface {
	Tool

	// Capability returns the tool's declared capabilities.
	Capability() ToolCapability

	// ClassifyAction inspects the tool arguments and returns the action's risk tier.
	// This is called by the guardrail engine before Execute.
	ClassifyAction(args map[string]interface{}) ActionClassification
}

// ProtectionClass defines a set of resources that require special protection.
// Protection classes are configurable per-environment or globally.
type ProtectionClass struct {
	// Name identifies this protection class (e.g. "kubernetes-data", "production-databases").
	Name string

	// Description explains what this class protects.
	Description string

	// Rules define the protection rules.
	Rules []ProtectionRule
}

// ProtectionRule specifies a single resource protection rule.
type ProtectionRule struct {
	// Domain is the tool domain this rule applies to (e.g. "kubernetes", "ssh", "sql").
	// Empty means all domains.
	Domain string

	// Pattern matches the action target (glob-style).
	// Examples: "PersistentVolumeClaim/*", "/etc/shadow", "DROP TABLE *"
	Pattern string

	// Action specifies what happens when a match is found.
	Action ProtectionAction

	// Description explains the rule.
	Description string
}

// ProtectionAction defines how a protection rule is enforced.
type ProtectionAction int

const (
	// ProtectionBlock unconditionally blocks the action.
	ProtectionBlock ProtectionAction = iota
	// ProtectionApprove requires human approval before proceeding.
	ProtectionApprove
	// ProtectionAudit allows the action but logs an audit event.
	ProtectionAudit
)

// String returns the human-readable name of a protection action.
func (a ProtectionAction) String() string {
	switch a {
	case ProtectionBlock:
		return "block"
	case ProtectionApprove:
		return "approve"
	case ProtectionAudit:
		return "audit"
	default:
		return "unknown"
	}
}

// DefaultKubernetesProtectionClass returns the built-in K8s protection rules.
// These ship as defaults and cannot be weakened — only extended.
func DefaultKubernetesProtectionClass() ProtectionClass {
	return ProtectionClass{
		Name:        "kubernetes-data",
		Description: "Protects Kubernetes data resources from automated deletion or modification",
		Rules: []ProtectionRule{
			{Domain: "kubernetes", Pattern: "delete persistentvolumeclaim/*", Action: ProtectionBlock, Description: "Never delete PVCs"},
			{Domain: "kubernetes", Pattern: "delete persistentvolume/*", Action: ProtectionBlock, Description: "Never delete PVs"},
			{Domain: "kubernetes", Pattern: "patch persistentvolume/*/spec/persistentVolumeReclaimPolicy", Action: ProtectionBlock, Description: "Never change PV reclaim policy"},
			{Domain: "kubernetes", Pattern: "delete namespace/*", Action: ProtectionBlock, Description: "Never delete namespaces"},
			{Domain: "kubernetes", Pattern: "delete clusters.postgresql.cnpg.io/*", Action: ProtectionBlock, Description: "Never delete CNPG clusters"},
			{Domain: "kubernetes", Pattern: "delete clusters.rds.aws.upbound.io/*", Action: ProtectionBlock, Description: "Never delete RDS clusters"},
		},
	}
}

// DefaultSSHProtectionClass returns built-in SSH protection rules.
func DefaultSSHProtectionClass() ProtectionClass {
	return ProtectionClass{
		Name:        "ssh-safety",
		Description: "Protects critical system files and prevents dangerous commands via SSH",
		Rules: []ProtectionRule{
			{Domain: "ssh", Pattern: "*/etc/shadow", Action: ProtectionBlock, Description: "Never read or modify shadow file"},
			{Domain: "ssh", Pattern: "*/etc/passwd", Action: ProtectionAudit, Description: "Audit access to passwd file"},
			{Domain: "ssh", Pattern: "*dd if=*", Action: ProtectionBlock, Description: "Block raw disk operations"},
			{Domain: "ssh", Pattern: "*mkfs*", Action: ProtectionBlock, Description: "Block filesystem creation"},
			{Domain: "ssh", Pattern: "*fdisk*", Action: ProtectionBlock, Description: "Block partition operations"},
			{Domain: "ssh", Pattern: "*rm -rf /*", Action: ProtectionBlock, Description: "Block recursive root deletion"},
			{Domain: "ssh", Pattern: "*> /dev/*", Action: ProtectionBlock, Description: "Block writes to device files"},
		},
	}
}
