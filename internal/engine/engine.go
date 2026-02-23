/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package engine implements the Action Sheet Engine — the safety-critical
// enforcement layer between LLM tool requests and actual execution.
//
// Every tool call passes through the engine before execution:
//  1. Match against declared Action Sheet
//  2. Classify action tier (read / service / destructive / data)
//  3. Check autonomy level
//  4. Check allow/deny lists
//  5. Check hardcoded data protection rules
//  6. Check data resource impact
//  7. Run pre-conditions
//  8. Check cooldown
//
// If any check fails, the action is BLOCKED. The LLM never sees the tool response.
package engine

import (
	"fmt"
	"strings"
	"sync"
	"time"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/resolver"
	"github.com/marcus-qen/legator/internal/safety/blastradius"
	"github.com/marcus-qen/legator/internal/skill"
	"github.com/marcus-qen/legator/internal/tools"
)

// Decision is the result of the engine evaluating a tool call.
type Decision struct {
	// Allowed is true if the action may proceed.
	Allowed bool

	// NeedsApproval is true if the action requires human approval before execution.
	// When true, Allowed is false but the action is not permanently blocked —
	// it should be submitted for approval.
	NeedsApproval bool

	// Status is the resulting action status.
	Status corev1alpha1.ActionStatus

	// Tier is the classified action tier.
	Tier corev1alpha1.ActionTier

	// PreFlight captures all pre-flight check results.
	PreFlight corev1alpha1.PreFlightResult

	// MatchedAction is the Action Sheet entry that matched (nil if undeclared).
	MatchedAction *skill.Action

	// BlockReason is a human-readable explanation when blocked.
	BlockReason string

	// BlastRadius is the deterministic pre-execution impact assessment.
	BlastRadius blastradius.Assessment
}

// Engine is the Action Sheet enforcement engine.
type Engine struct {
	guardrails       *corev1alpha1.GuardrailsSpec
	actionRegistry   map[string]*skill.Action
	dataIndex        *resolver.DataResourceIndex
	cooldowns        *CooldownTracker
	protectionEngine *tools.ProtectionEngine
	toolRegistry      *tools.Registry
	agentName         string
	blastRadiusScorer blastradius.Scorer
	actorRoles        []string
}

// NewEngine creates an engine for a specific agent run.
func NewEngine(
	agentName string,
	guardrails *corev1alpha1.GuardrailsSpec,
	actionRegistry map[string]*skill.Action,
	dataIndex *resolver.DataResourceIndex,
) *Engine {
	return &Engine{
		agentName:         agentName,
		guardrails:        guardrails,
		actionRegistry:    actionRegistry,
		dataIndex:         dataIndex,
		cooldowns:         NewCooldownTracker(),
		blastRadiusScorer: blastradius.NewDeterministicScorer(),
		actorRoles:        []string{"operator"},
	}
}

// WithProtectionEngine adds a configurable protection engine for domain-agnostic guardrails.
func (e *Engine) WithProtectionEngine(pe *tools.ProtectionEngine) *Engine {
	e.protectionEngine = pe
	return e
}

// WithToolRegistry adds a tool registry for ClassifiableTool-based action classification.
func (e *Engine) WithToolRegistry(reg *tools.Registry) *Engine {
	e.toolRegistry = reg
	return e
}

// WithBlastRadiusScorer overrides the default blast-radius scorer.
func (e *Engine) WithBlastRadiusScorer(scorer blastradius.Scorer) *Engine {
	e.blastRadiusScorer = scorer
	return e
}

// WithActorRoles sets actor roles used by blast-radius policy evaluation.
func (e *Engine) WithActorRoles(roles []string) *Engine {
	if len(roles) == 0 {
		e.actorRoles = []string{"operator"}
		return e
	}
	e.actorRoles = roles
	return e
}

// Evaluate runs all pre-flight checks for a tool call.
// This is the single entry point — all safety enforcement happens here.
func (e *Engine) Evaluate(toolName string, target string) *Decision {
	d := &Decision{
		Allowed: true,
		Status:  corev1alpha1.ActionStatusExecuted,
	}

	// Step 1: Match against Action Sheet
	matched := e.matchAction(toolName, target)
	d.MatchedAction = matched

	// Step 2: Classify tier
	if matched != nil {
		d.Tier = classifyTier(matched.Tier)
	} else {
		// Undeclared action — classify from tool name heuristics
		d.Tier = classifyFromToolName(toolName)
	}

	// Step 2.5: Compute blast-radius assessment (deterministic, side-effect free)
	d.BlastRadius = e.assessBlastRadius(toolName, target, d.Tier)

	// Step 3: Check hardcoded data protection rules (non-configurable)
	if blocked, reason := checkDataProtection(toolName, target); blocked {
		d.Allowed = false
		d.Status = corev1alpha1.ActionStatusBlocked
		d.Tier = corev1alpha1.ActionTierDataMutation
		d.PreFlight.DataProtection = "BLOCKED"
		d.PreFlight.Reason = reason
		d.BlockReason = reason
		return d
	}
	d.PreFlight.DataProtection = "pass"

	// Step 3b: Check configurable protection classes (extends hardcoded rules)
	if e.protectionEngine != nil {
		domain := inferDomain(toolName)
		result := e.protectionEngine.Evaluate(domain, toolName+" "+target)
		if !result.Allowed {
			reason := fmt.Sprintf("PROTECTION CLASS %q: %s", result.MatchedClass, result.MatchedRule.Description)
			d.Allowed = false
			d.Status = corev1alpha1.ActionStatusBlocked
			d.PreFlight.DataProtection = "BLOCKED (protection class)"
			d.PreFlight.Reason = reason
			d.BlockReason = reason
			return d
		}
	}

	// Step 3c: Use ClassifiableTool for fine-grained classification when available
	if e.toolRegistry != nil {
		if tool, found := e.toolRegistry.Get(toolName); found {
			if ct, ok := tool.(tools.ClassifiableTool); ok {
				// Build args map from target (tool-specific parsing)
				args := map[string]interface{}{"command": target, "host": target}
				classification := ct.ClassifyAction(args)
				if classification.Blocked {
					d.Allowed = false
					d.Status = corev1alpha1.ActionStatusBlocked
					d.Tier = corev1alpha1.ActionTierDataMutation
					d.PreFlight.DataProtection = "BLOCKED (tool classification)"
					d.PreFlight.Reason = classification.BlockReason
					d.BlockReason = classification.BlockReason
					return d
				}
				// Use the tool's own classification if it returned a concrete tier
				d.Tier = mapToolTierToAPITier(classification.Tier)
			}
		}
	}

	// Step 4: Check data resource impact
	if e.dataIndex != nil {
		if blocked, reason := checkDataResourceImpact(toolName, target, d.Tier, e.dataIndex); blocked {
			d.Allowed = false
			d.Status = corev1alpha1.ActionStatusBlocked
			d.PreFlight.DataImpactCheck = "BLOCKED"
			d.PreFlight.Reason = reason
			d.BlockReason = reason
			return d
		}
	}
	d.PreFlight.DataImpactCheck = "pass"

	// Step 5: Check autonomy level
	if blocked, reason := checkAutonomy(d.Tier, e.guardrails.Autonomy); blocked {
		// If approval mode is configured, request approval instead of hard block
		if e.guardrails.ApprovalMode != "" && e.guardrails.ApprovalMode != "none" {
			d.Allowed = false
			d.NeedsApproval = true
			d.Status = corev1alpha1.ActionStatusPendingApproval
			d.PreFlight.AutonomyCheck = "NEEDS_APPROVAL"
			d.PreFlight.Reason = reason
			d.BlockReason = reason
			return d
		}
		d.Allowed = false
		d.Status = corev1alpha1.ActionStatusBlocked
		d.PreFlight.AutonomyCheck = "BLOCKED"
		d.PreFlight.Reason = reason
		d.BlockReason = reason
		return d
	}
	d.PreFlight.AutonomyCheck = "pass"

	// Step 6: Check deny list (overrides allow list)
	if blocked, reason := checkDenyList(toolName, target, e.guardrails.DeniedActions); blocked {
		d.Allowed = false
		d.Status = corev1alpha1.ActionStatusBlocked
		d.PreFlight.AllowListCheck = "BLOCKED (deny list)"
		d.PreFlight.Reason = reason
		d.BlockReason = reason
		return d
	}

	// Step 7: Check allow list (only for mutation actions)
	if d.Tier != corev1alpha1.ActionTierRead {
		if blocked, reason := checkAllowList(toolName, target, e.guardrails.AllowedActions); blocked {
			d.Allowed = false
			d.Status = corev1alpha1.ActionStatusBlocked
			d.PreFlight.AllowListCheck = "BLOCKED (not in allow list)"
			d.PreFlight.Reason = reason
			d.BlockReason = reason
			return d
		}
	}
	d.PreFlight.AllowListCheck = "pass"

	// Step 8: Check cooldown
	if matched != nil && matched.Cooldown != "" {
		if blocked, reason := e.checkCooldown(matched.ID, target, matched.Cooldown); blocked {
			d.Allowed = false
			d.Status = corev1alpha1.ActionStatusSkipped
			d.BlockReason = reason
			return d
		}
	}

	// Step 9: Check undeclared action (allowlist principle)
	if matched == nil && d.Tier != corev1alpha1.ActionTierRead {
		// Undeclared mutations are denied
		d.Allowed = false
		d.Status = corev1alpha1.ActionStatusBlocked
		d.BlockReason = fmt.Sprintf("undeclared mutation action %q — not in Action Sheet", toolName)
		return d
	}

	return d
}

// RecordExecution records that an action was executed (for cooldown tracking).
func (e *Engine) RecordExecution(actionID, target string) {
	e.cooldowns.Record(e.agentName, actionID, target)
}

// --- Matcher (Step 2.6) ---

// matchAction finds the Action Sheet entry that matches a tool call.
func (e *Engine) matchAction(toolName, target string) *skill.Action {
	for id, action := range e.actionRegistry {
		_ = id
		if matchToolAction(action, toolName, target) {
			return action
		}
	}
	return nil
}

// matchToolAction checks if a tool call matches an Action Sheet entry.
func matchToolAction(action *skill.Action, toolName, target string) bool {
	// Exact tool match
	if action.Tool == toolName {
		// If target pattern specified, check it
		if action.TargetPattern != "" {
			return matchGlob(action.TargetPattern, target)
		}
		return true
	}

	// Glob tool match (e.g. "kubectl.*")
	if matchGlob(action.Tool, toolName) {
		if action.TargetPattern != "" {
			return matchGlob(action.TargetPattern, target)
		}
		return true
	}

	return false
}

// matchGlob performs simple glob matching (* matches any sequence of characters).
func matchGlob(pattern, text string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == text
	}

	// Check prefix
	if parts[0] != "" && !strings.HasPrefix(text, parts[0]) {
		return false
	}

	remaining := text
	if parts[0] != "" {
		remaining = remaining[len(parts[0]):]
	}

	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		idx := strings.Index(remaining, parts[i])
		if idx < 0 {
			return false
		}
		remaining = remaining[idx+len(parts[i]):]
	}

	// If pattern ends with non-*, remaining must be empty
	if parts[len(parts)-1] != "" {
		return len(remaining) == 0
	}

	return true
}

// --- Classifier (Step 2.7) ---

// classifyTier converts a string tier to the typed enum.
func classifyTier(tier string) corev1alpha1.ActionTier {
	switch tier {
	case "read":
		return corev1alpha1.ActionTierRead
	case "service-mutation":
		return corev1alpha1.ActionTierServiceMutation
	case "destructive-mutation":
		return corev1alpha1.ActionTierDestructiveMutation
	case "data-mutation":
		return corev1alpha1.ActionTierDataMutation
	default:
		// Unknown tier defaults to destructive (conservative)
		return corev1alpha1.ActionTierDestructiveMutation
	}
}

// classifyFromToolName infers an action tier from tool name heuristics.
// Used for undeclared actions that aren't in the Action Sheet.
func classifyFromToolName(toolName string) corev1alpha1.ActionTier {
	lower := strings.ToLower(toolName)

	// Read operations
	readPatterns := []string{"get", "list", "describe", "logs", "read", "fetch", "check", "status", "analyze"}
	for _, p := range readPatterns {
		if strings.Contains(lower, p) {
			return corev1alpha1.ActionTierRead
		}
	}

	// Destructive operations
	destructivePatterns := []string{"delete", "destroy", "remove", "purge", "drain", "cordon"}
	for _, p := range destructivePatterns {
		if strings.Contains(lower, p) {
			return corev1alpha1.ActionTierDestructiveMutation
		}
	}

	// Service mutations (default for unknown mutations)
	return corev1alpha1.ActionTierServiceMutation
}

// --- Autonomy Enforcer (Step 2.8) ---

// autonomyRank maps autonomy levels to numerical ranks for comparison.
func autonomyRank(level corev1alpha1.AutonomyLevel) int {
	switch level {
	case corev1alpha1.AutonomyObserve:
		return 0
	case corev1alpha1.AutonomyRecommend:
		return 1
	case corev1alpha1.AutonomySafe:
		return 2
	case corev1alpha1.AutonomyDestructive:
		return 3
	default:
		return 0
	}
}

// requiredAutonomy returns the minimum autonomy level for an action tier.
func requiredAutonomy(tier corev1alpha1.ActionTier) corev1alpha1.AutonomyLevel {
	switch tier {
	case corev1alpha1.ActionTierRead:
		return corev1alpha1.AutonomyObserve
	case corev1alpha1.ActionTierServiceMutation:
		return corev1alpha1.AutonomySafe
	case corev1alpha1.ActionTierDestructiveMutation:
		return corev1alpha1.AutonomyDestructive
	case corev1alpha1.ActionTierDataMutation:
		// Data mutations are NEVER allowed regardless of autonomy
		return "never"
	default:
		return corev1alpha1.AutonomyDestructive
	}
}

func checkAutonomy(tier corev1alpha1.ActionTier, agentAutonomy corev1alpha1.AutonomyLevel) (blocked bool, reason string) {
	// Data mutations are always blocked — no autonomy level unlocks them
	if tier == corev1alpha1.ActionTierDataMutation {
		return true, "data mutations are unconditionally blocked — no autonomy level unlocks data operations"
	}

	required := requiredAutonomy(tier)
	if autonomyRank(agentAutonomy) < autonomyRank(required) {
		return true, fmt.Sprintf("action requires autonomy level %q but agent has %q",
			required, agentAutonomy)
	}

	return false, ""
}

// --- Allow/Deny List Enforcer (Step 2.9) ---

func checkDenyList(toolName, target string, deniedActions []string) (blocked bool, reason string) {
	combined := toolName
	if target != "" {
		combined = toolName + " " + target
	}

	for _, pattern := range deniedActions {
		if matchGlob(pattern, combined) || matchGlob(pattern, toolName) {
			return true, fmt.Sprintf("action %q matches deny pattern %q", toolName, pattern)
		}
	}
	return false, ""
}

func checkAllowList(toolName, target string, allowedActions []string) (blocked bool, reason string) {
	// Empty allow list = all allowed (no restriction)
	if len(allowedActions) == 0 {
		return false, ""
	}

	combined := toolName
	if target != "" {
		combined = toolName + " " + target
	}

	for _, pattern := range allowedActions {
		if matchGlob(pattern, combined) || matchGlob(pattern, toolName) {
			return false, ""
		}
	}

	return true, fmt.Sprintf("action %q does not match any allow pattern", toolName)
}

// --- Cooldown Tracker (Step 2.11) ---

// CooldownTracker tracks when actions were last executed.
type CooldownTracker struct {
	mu      sync.Mutex
	records map[string]time.Time // key: "agent/action/target"
}

// NewCooldownTracker creates a new tracker.
func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{
		records: make(map[string]time.Time),
	}
}

// Record marks that an action was executed now.
func (t *CooldownTracker) Record(agent, actionID, target string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := fmt.Sprintf("%s/%s/%s", agent, actionID, target)
	t.records[key] = time.Now()
}

// Check returns true if the action is within its cooldown period.
func (t *CooldownTracker) Check(agent, actionID, target string, cooldownDuration time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := fmt.Sprintf("%s/%s/%s", agent, actionID, target)
	last, ok := t.records[key]
	if !ok {
		return false
	}
	return time.Since(last) < cooldownDuration
}

// inferDomain extracts the tool domain from a tool name.
// "kubectl.get" → "kubernetes", "ssh.exec" → "ssh", "http.get" → "http"
func inferDomain(toolName string) string {
	lower := strings.ToLower(toolName)
	if strings.HasPrefix(lower, "kubectl") {
		return "kubernetes"
	}
	if strings.HasPrefix(lower, "ssh") {
		return "ssh"
	}
	if strings.HasPrefix(lower, "http") {
		return "http"
	}
	if strings.HasPrefix(lower, "sql") {
		return "sql"
	}
	if strings.HasPrefix(lower, "mcp.") {
		// MCP tools: mcp.<server>.<tool> — use server as domain
		parts := strings.SplitN(lower, ".", 3)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return "unknown"
}

// mapToolTierToAPITier converts internal tool tiers to the API tier type.
func mapToolTierToAPITier(tier tools.ActionTier) corev1alpha1.ActionTier {
	switch tier {
	case tools.TierRead:
		return corev1alpha1.ActionTierRead
	case tools.TierServiceMutation:
		return corev1alpha1.ActionTierServiceMutation
	case tools.TierDestructiveMutation:
		return corev1alpha1.ActionTierDestructiveMutation
	case tools.TierDataMutation:
		return corev1alpha1.ActionTierDataMutation
	default:
		return corev1alpha1.ActionTierServiceMutation
	}
}

func (e *Engine) assessBlastRadius(toolName, target string, tier corev1alpha1.ActionTier) blastradius.Assessment {
	if e.blastRadiusScorer == nil {
		e.blastRadiusScorer = blastradius.NewDeterministicScorer()
	}

	domain := inferDomain(toolName)
	return e.blastRadiusScorer.Assess(blastradius.Input{
		Tier:          tier,
		MutationDepth: inferMutationDepth(domain, tier),
		ActorRoles:    e.actorRoles,
		Targets: []blastradius.Target{
			{
				Kind:        domain,
				Name:        target,
				Environment: inferEnvironment(target),
				Domain:      domain,
			},
		},
	})
}

func inferMutationDepth(domain string, tier corev1alpha1.ActionTier) blastradius.MutationDepth {
	switch domain {
	case "sql":
		return blastradius.MutationDepthData
	case "kubernetes", "ssh":
		return blastradius.MutationDepthService
	case "http":
		if tier == corev1alpha1.ActionTierDestructiveMutation || tier == corev1alpha1.ActionTierDataMutation {
			return blastradius.MutationDepthIdentity
		}
		return blastradius.MutationDepthNetwork
	default:
		switch tier {
		case corev1alpha1.ActionTierDataMutation:
			return blastradius.MutationDepthData
		case corev1alpha1.ActionTierDestructiveMutation:
			return blastradius.MutationDepthIdentity
		default:
			return blastradius.MutationDepthService
		}
	}
}

func inferEnvironment(target string) string {
	lower := strings.ToLower(target)
	switch {
	case strings.Contains(lower, "prod") || strings.Contains(lower, "production"):
		return "prod"
	case strings.Contains(lower, "stage") || strings.Contains(lower, "staging"):
		return "staging"
	default:
		return "dev"
	}
}

func (e *Engine) checkCooldown(actionID, target, cooldownStr string) (blocked bool, reason string) {
	dur, err := time.ParseDuration(cooldownStr)
	if err != nil {
		// Invalid cooldown = don't block, just warn
		return false, ""
	}

	if e.cooldowns.Check(e.agentName, actionID, target, dur) {
		return true, fmt.Sprintf("action %q on target %q is within cooldown period (%s)",
			actionID, target, cooldownStr)
	}
	return false, ""
}
