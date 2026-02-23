/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package rbac

import (
	"context"
	"fmt"
	"strings"
)

// Role defines the built-in RBAC roles.
type Role string

const (
	// RoleViewer can see all status, logs, and audit trails.
	RoleViewer Role = "viewer"

	// RoleOperator can run agents, approve/deny within scope, and investigate.
	RoleOperator Role = "operator"

	// RoleAdmin can do everything, including destructive mutations and configuration.
	RoleAdmin Role = "admin"
)

// Action represents a specific API action to authorize.
type Action string

const (
	ActionViewAgents    Action = "agents:view"
	ActionViewRuns      Action = "runs:view"
	ActionViewInventory Action = "inventory:view"
	ActionViewAudit     Action = "audit:view"
	ActionRunAgent      Action = "agents:run"
	ActionAbortRun      Action = "runs:abort"
	ActionApprove       Action = "approvals:decide"
	ActionManageDevice  Action = "inventory:manage"
	ActionConfigure     Action = "config:write"
	ActionChat          Action = "chat:use"
)

// MaxAutonomy defines the maximum autonomy level a role can grant.
type MaxAutonomy string

const (
	MaxAutonomyObserve      MaxAutonomy = "observe"
	MaxAutonomyRecommend    MaxAutonomy = "recommend"
	MaxAutonomyAutomateSafe MaxAutonomy = "automate-safe"
	MaxAutonomyAutomateAll  MaxAutonomy = "automate-destructive"
)

// UserIdentity represents an authenticated user extracted from OIDC claims.
type UserIdentity struct {
	// Subject is the OIDC "sub" claim — unique user identifier.
	Subject string

	// Email is the user's email address.
	Email string

	// Name is the user's display name.
	Name string

	// Groups are the OIDC group claims (Keycloak realm roles or groups).
	Groups []string

	// Claims contains all raw OIDC claims for attribute-based matching.
	Claims map[string]interface{}
}

// UserPolicy defines what a user or group can do.
type UserPolicy struct {
	// Name is a human-readable policy name.
	Name string

	// Subjects match on OIDC claims.
	Subjects []SubjectMatcher

	// Role is the granted role.
	Role Role

	// Scope limits what the role applies to.
	Scope PolicyScope
}

// SubjectMatcher matches a user based on an OIDC claim.
type SubjectMatcher struct {
	// Claim is the OIDC claim name (e.g., "email", "groups").
	Claim string

	// Value is the required value (exact match or glob pattern).
	Value string
}

// PolicyScope limits the blast radius of a role grant.
type PolicyScope struct {
	// Tags limits to devices/agents with these tags. Empty = all (if role permits).
	Tags []string

	// Namespaces limits to these K8s namespaces. Empty = all.
	Namespaces []string

	// Agents limits to these agent names. Supports glob. Empty = all.
	Agents []string

	// MaxAutonomy caps the maximum autonomy level the user can request.
	MaxAutonomy MaxAutonomy
}

// Decision is the result of an authorization check.
type Decision struct {
	Allowed bool
	Reason  string
	// EffectiveScope is the intersection of the user's scope and the request scope.
	EffectiveScope PolicyScope
}

// Engine evaluates RBAC policies for authorization decisions.
type Engine struct {
	policies []UserPolicy
}

// NewEngine creates an RBAC engine with the given policies.
func NewEngine(policies []UserPolicy) *Engine {
	return &Engine{policies: policies}
}

// ResolvePolicy returns the best matching policy for a user using deterministic
// ordering (highest role rank, then lexicographic policy name).
func (e *Engine) ResolvePolicy(user *UserIdentity) (*UserPolicy, bool) {
	if user == nil {
		return nil, false
	}

	matched := e.matchingPolicies(user)
	if len(matched) == 0 {
		return nil, false
	}

	best := matched[0]
	for i := 1; i < len(matched); i++ {
		candidate := matched[i]
		if roleRank(candidate.Role) > roleRank(best.Role) {
			best = candidate
			continue
		}
		if roleRank(candidate.Role) == roleRank(best.Role) && candidate.Name < best.Name {
			best = candidate
		}
	}

	return &best, true
}

// Authorize checks whether the given user can perform the action.
func (e *Engine) Authorize(_ context.Context, user *UserIdentity, action Action, resource string) Decision {
	if user == nil {
		return Decision{Allowed: false, Reason: "no user identity"}
	}

	bestPolicy, ok := e.ResolvePolicy(user)
	if !ok {
		return Decision{Allowed: false, Reason: fmt.Sprintf("no policy matches user %s", user.Email)}
	}

	if !rolePermits(bestPolicy.Role, action) {
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("role %s does not permit action %s", bestPolicy.Role, action),
		}
	}

	if resource != "" && !inScope(bestPolicy.Scope, resource) {
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("resource %s is outside policy scope", resource),
		}
	}

	return Decision{
		Allowed:        true,
		Reason:         fmt.Sprintf("permitted by policy %s (role: %s)", bestPolicy.Name, bestPolicy.Role),
		EffectiveScope: bestPolicy.Scope,
	}
}

// ComposeDecision deterministically combines a base RBAC decision with a
// matching UserPolicy decision, ensuring UserPolicy cannot bypass RBAC.
func ComposeDecision(
	baseDecision Decision,
	basePolicy *UserPolicy,
	overlayPolicy *UserPolicy,
	action Action,
	resource string,
) Decision {
	if !baseDecision.Allowed {
		return baseDecision
	}
	if basePolicy == nil {
		return Decision{Allowed: false, Reason: "base policy resolution failed"}
	}
	if overlayPolicy == nil {
		return baseDecision
	}

	effectiveRole := clampRole(basePolicy.Role, overlayPolicy.Role)
	effectiveScope := mergedScope(basePolicy.Scope, overlayPolicy.Scope)

	if !rolePermits(effectiveRole, action) {
		return Decision{
			Allowed: false,
			Reason: fmt.Sprintf(
				"denied by composed policy: base=%s role=%s, userPolicy=%s role=%s, effectiveRole=%s blocks action %s",
				basePolicy.Name,
				basePolicy.Role,
				overlayPolicy.Name,
				overlayPolicy.Role,
				effectiveRole,
				action,
			),
			EffectiveScope: effectiveScope,
		}
	}

	if resource != "" && !inScope(overlayPolicy.Scope, resource) {
		return Decision{
			Allowed: false,
			Reason: fmt.Sprintf(
				"denied by user policy scope: resource %s outside userPolicy %s",
				resource,
				overlayPolicy.Name,
			),
			EffectiveScope: effectiveScope,
		}
	}

	return Decision{
		Allowed: true,
		Reason: fmt.Sprintf(
			"permitted by composed policy: base=%s(role:%s) + userPolicy=%s(role:%s) => effectiveRole=%s",
			basePolicy.Name,
			basePolicy.Role,
			overlayPolicy.Name,
			overlayPolicy.Role,
			effectiveRole,
		),
		EffectiveScope: effectiveScope,
	}
}

func (e *Engine) matchingPolicies(user *UserIdentity) []UserPolicy {
	matched := make([]UserPolicy, 0, len(e.policies))
	for _, p := range e.policies {
		if e.matchesSubject(user, p.Subjects) {
			matched = append(matched, p)
		}
	}
	return matched
}

// matchesSubject checks if the user matches any of the subject matchers.
func (e *Engine) matchesSubject(user *UserIdentity, subjects []SubjectMatcher) bool {
	for _, s := range subjects {
		switch s.Claim {
		case "email":
			if matchGlob(user.Email, s.Value) {
				return true
			}
		case "sub", "subject":
			if user.Subject == s.Value {
				return true
			}
		case "groups", "group":
			for _, g := range user.Groups {
				if matchGlob(g, s.Value) {
					return true
				}
			}
		default:
			// Check raw claims
			if v, ok := user.Claims[s.Claim]; ok {
				if fmt.Sprintf("%v", v) == s.Value {
					return true
				}
			}
		}
	}
	return false
}

// inScope checks whether a resource is within the policy scope.
func inScope(scope PolicyScope, resource string) bool {
	// If no scope restrictions, everything is in scope
	if len(scope.Tags) == 0 && len(scope.Namespaces) == 0 && len(scope.Agents) == 0 {
		return true
	}

	// Check agents scope
	if len(scope.Agents) > 0 {
		for _, pattern := range scope.Agents {
			if matchGlob(resource, pattern) {
				return true
			}
		}
	}

	// Check tags scope
	if len(scope.Tags) > 0 {
		for _, pattern := range scope.Tags {
			if matchGlob(resource, pattern) {
				return true
			}
		}
	}

	// Check namespaces
	if len(scope.Namespaces) > 0 {
		for _, pattern := range scope.Namespaces {
			if matchGlob(resource, pattern) {
				return true
			}
		}
	}

	return false
}

func clampRole(base Role, overlay Role) Role {
	if roleRank(overlay) < roleRank(base) {
		return overlay
	}
	return base
}

func mergedScope(base PolicyScope, overlay PolicyScope) PolicyScope {
	effective := base
	if len(overlay.Tags) > 0 {
		effective.Tags = append([]string(nil), overlay.Tags...)
	}
	if len(overlay.Namespaces) > 0 {
		effective.Namespaces = append([]string(nil), overlay.Namespaces...)
	}
	if len(overlay.Agents) > 0 {
		effective.Agents = append([]string(nil), overlay.Agents...)
	}
	if overlay.MaxAutonomy != "" && autonomyRank(overlay.MaxAutonomy) < autonomyRank(base.MaxAutonomy) {
		effective.MaxAutonomy = overlay.MaxAutonomy
	}
	return effective
}

func autonomyRank(a MaxAutonomy) int {
	switch a {
	case MaxAutonomyObserve:
		return 1
	case MaxAutonomyRecommend:
		return 2
	case MaxAutonomyAutomateSafe:
		return 3
	case MaxAutonomyAutomateAll:
		return 4
	default:
		return 0
	}
}

// roleRank returns a numeric rank for role comparison (higher = more privilege).
func roleRank(r Role) int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	default:
		return 0
	}
}

// rolePermits checks if a role is allowed to perform an action.
func rolePermits(r Role, action Action) bool {
	switch r {
	case RoleAdmin:
		return true // Admin can do everything
	case RoleOperator:
		switch action {
		case ActionViewAgents, ActionViewRuns, ActionViewInventory, ActionViewAudit,
			ActionRunAgent, ActionAbortRun, ActionApprove, ActionChat:
			return true
		default:
			return false
		}
	case RoleViewer:
		switch action {
		case ActionViewAgents, ActionViewRuns, ActionViewInventory, ActionViewAudit:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// matchGlob performs simple glob matching with * wildcard.
func matchGlob(s, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(s, pattern[:len(pattern)-1])
	}
	return s == pattern
}
