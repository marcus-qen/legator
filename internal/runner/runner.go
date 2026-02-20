/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package runner orchestrates agent execution: prompt assembly → LLM conversation →
// tool execution → guardrail enforcement → audit trail recording.
//
// This is the central loop:
//  1. Create LegatorRun CR (Pending)
//  2. Assemble prompt via assembler
//  3. Enter conversation loop:
//     a. Send to LLM
//     b. If tool_use: evaluate each tool call through engine, execute or block
//     c. Feed results back to LLM
//     d. Repeat until end_turn or budget exhausted
//  4. Record findings, usage, guardrail summary
//  5. Mark LegatorRun terminal (Succeeded/Failed/Escalated/Blocked)
package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/assembler"
	"github.com/marcus-qen/legator/internal/engine"
	"github.com/marcus-qen/legator/internal/metrics"
	"github.com/marcus-qen/legator/internal/provider"
	"github.com/marcus-qen/legator/internal/security"
	"github.com/marcus-qen/legator/internal/telemetry"
	"github.com/marcus-qen/legator/internal/tools"
)

// Runner executes a single agent run from start to finish.
type Runner struct {
	client    client.Client
	assembler *assembler.Assembler
	log       logr.Logger
}

// NewRunner creates a runner.
func NewRunner(c client.Client, asm *assembler.Assembler, log logr.Logger) *Runner {
	return &Runner{
		client:    c,
		assembler: asm,
		log:       log,
	}
}

// RunConfig holds runtime parameters for a single execution.
type RunConfig struct {
	// Provider is the LLM provider to use.
	Provider provider.Provider

	// ToolRegistry holds all available tools.
	ToolRegistry *tools.Registry

	// Trigger describes what initiated this run.
	Trigger corev1alpha1.RunTrigger

	// Cleanup is called when the run ends (success or failure).
	// Use this to revoke dynamic credentials, close connections, etc.
	// Errors are logged but don't affect the run result.
	Cleanup func(ctx context.Context) []error
}

// Execute runs a full agent lifecycle.
// It creates an LegatorRun CR, assembles the prompt, enters the tool-use loop,
// and records the complete audit trail.
func (r *Runner) Execute(ctx context.Context, agent *corev1alpha1.LegatorAgent, cfg RunConfig) (*corev1alpha1.LegatorRun, error) {
	startTime := time.Now()

	// Telemetry: parent span for the entire run
	ctx, runSpan := telemetry.StartRunSpan(ctx, agent.Name, string(cfg.Trigger))
	defer runSpan.End()

	// Metrics: track active runs
	metrics.ActiveRuns.Inc()
	defer metrics.ActiveRuns.Dec()

	// Parse wall-clock timeout
	timeout, err := time.ParseDuration(agent.Spec.Model.Timeout)
	if err != nil {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Step 1: Assemble the agent
	asmCtx, asmSpan := telemetry.StartAssemblySpan(ctx, agent.Name)
	assembled, err := r.assembler.Assemble(asmCtx, agent)
	if err != nil {
		asmSpan.RecordError(err)
		asmSpan.SetStatus(codes.Error, "assembly failed")
		asmSpan.End()
		run := r.createFailedRun(agent, cfg.Trigger, startTime, fmt.Sprintf("assembly failed: %v", err))
		return run, err
	}
	asmSpan.End()

	// Step 2: Create LegatorRun CR
	run := r.createLegatorRun(agent, assembled, cfg.Trigger)
	if err := r.client.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("create LegatorRun: %w", err)
	}

	// Step 3: Mark as Running
	run.Status.Phase = corev1alpha1.RunPhaseRunning
	run.Status.StartTime = &metav1.Time{Time: startTime}
	if err := r.client.Status().Update(ctx, run); err != nil {
		r.log.Error(err, "failed to update LegatorRun status to Running")
	}

	// Step 4: Create the engine
	eng := engine.NewEngine(
		agent.Name,
		&agent.Spec.Guardrails,
		assembled.ActionRegistry,
		assembled.Environment.DataIndex,
	)

	// Step 5: Execute the conversation loop
	result := r.conversationLoop(ctx, assembled, eng, cfg, agent)

	// Step 6: Finalize the LegatorRun (use fresh context — run ctx may be expired)
	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer finalizeCancel()
	r.finalizeRun(finalizeCtx, run, result, startTime, agent, assembled)

	// Step 7: Cleanup dynamic credentials (Vault leases, ephemeral keys, etc.)
	if cfg.Cleanup != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if errs := cfg.Cleanup(cleanupCtx); len(errs) > 0 {
			for _, err := range errs {
				r.log.Error(err, "credential cleanup error", "agent", agent.Name)
			}
		}
	}

	return run, nil
}

// conversationResult captures the outcome of the tool-use conversation loop.
type conversationResult struct {
	actions    []corev1alpha1.ActionRecord
	findings   []corev1alpha1.RunFinding
	report     string
	phase      corev1alpha1.RunPhase
	totalIn    int64
	totalOut   int64
	iterations int32
	guardrails corev1alpha1.GuardrailSummary
	err        error
}

func (r *Runner) conversationLoop(
	ctx context.Context,
	assembled *assembler.AssembledAgent,
	eng *engine.Engine,
	cfg RunConfig,
	agent *corev1alpha1.LegatorAgent,
) *conversationResult {
	result := &conversationResult{
		phase: corev1alpha1.RunPhaseSucceeded,
		guardrails: corev1alpha1.GuardrailSummary{
			AutonomyCeiling: agent.Spec.Guardrails.Autonomy,
		},
	}

	maxIterations := agent.Spec.Guardrails.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 10
	}

	tokenBudget := agent.Spec.Model.TokenBudget
	if tokenBudget <= 0 {
		tokenBudget = 50000
	}

	// Build the initial message set
	messages := []provider.Message{
		{Role: "user", Content: "Execute your task now. Follow your skill instructions and report findings."},
	}

	var actionSeq int32

	for iteration := int32(0); iteration < maxIterations; iteration++ {
		result.iterations = iteration + 1

		// Budget check (tokens)
		if result.totalIn+result.totalOut >= tokenBudget {
			result.phase = corev1alpha1.RunPhaseFailed
			result.report = fmt.Sprintf("token budget exhausted: %d/%d used", result.totalIn+result.totalOut, tokenBudget)
			result.guardrails.BudgetUsed = r.buildBudgetUsage(result, tokenBudget, maxIterations, agent)
			break
		}

		// On the last iteration, withhold tools to force the LLM to produce
		// a final report instead of making another tool call.
		var iterTools []provider.ToolDefinition
		if iteration < maxIterations-1 {
			iterTools = cfg.ToolRegistry.Definitions()
		} else {
			// Last iteration: inject a "produce your report now" nudge
			messages = append(messages, provider.Message{
				Role:    "user",
				Content: "You have used all available tool calls. Produce your final report NOW based on the data you have already collected. Do not request any more tools.",
			})
		}

		// Call LLM (with tracing)
		llmCtx, llmSpan := telemetry.StartLLMCallSpan(ctx, assembled.Model.Model, assembled.Model.Provider, int(iteration))
		resp, err := cfg.Provider.Complete(llmCtx, &provider.CompletionRequest{
			SystemPrompt: assembled.Prompt,
			Messages:     messages,
			Tools:        iterTools,
			Model:        assembled.Model.Model,
			MaxTokens:    capMaxTokens(int32(tokenBudget - result.totalIn - result.totalOut)),
		})
		if err != nil {
			llmSpan.RecordError(err)
			llmSpan.End()
			if ctx.Err() != nil {
				result.phase = corev1alpha1.RunPhaseFailed
				result.report = "wall-clock timeout exceeded"
			} else {
				result.phase = corev1alpha1.RunPhaseFailed
				result.report = fmt.Sprintf("LLM call failed: %v", err)
			}
			result.err = err
			break
		}
		telemetry.EndLLMCallSpan(llmSpan, resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.HasToolCalls())

		// Track usage
		result.totalIn += resp.Usage.InputTokens
		result.totalOut += resp.Usage.OutputTokens

		// If no tool calls, this is the final response
		if !resp.HasToolCalls() {
			// Capture final text as report
			result.report = resp.Content

			// Parse findings from response
			result.findings = append(result.findings, extractFindings(resp.Content)...)

			// Add assistant message to history
			messages = append(messages, provider.Message{
				Role:    "assistant",
				Content: resp.Content,
			})
			break
		}

		// Process tool calls
		assistantMsg := provider.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		var toolResults []provider.ToolResult
		for _, tc := range resp.ToolCalls {
			actionSeq++
			now := metav1.Now()

			// Extract target for engine evaluation
			target := tools.ExtractTarget(tc.Name, tc.Args)

			// Telemetry: span per tool call
			_, toolSpan := telemetry.StartToolCallSpan(ctx, tc.Name, target, "")

			// Run through the engine (all safety checks)
			decision := eng.Evaluate(tc.Name, target)
			result.guardrails.ChecksPerformed++

			record := corev1alpha1.ActionRecord{
				Seq:       actionSeq,
				Timestamp: now,
				Tool:      tc.Name,
				Target:    target,
				Tier:      decision.Tier,
				PreFlightCheck: &corev1alpha1.PreFlightResult{
					AutonomyCheck:   decision.PreFlight.AutonomyCheck,
					DataImpactCheck: decision.PreFlight.DataImpactCheck,
					AllowListCheck:  decision.PreFlight.AllowListCheck,
					DataProtection:  decision.PreFlight.DataProtection,
					Reason:          decision.PreFlight.Reason,
				},
			}

			if !decision.Allowed {
				// Action blocked
				record.Status = decision.Status
				record.Result = decision.BlockReason
				result.guardrails.ActionsBlocked++

				// Metrics: record the block
				metrics.RecordGuardrailBlock(agent.Name, tc.Name)

				r.log.Info("action blocked",
					"agent", agent.Name,
					"tool", tc.Name,
					"target", target,
					"reason", decision.BlockReason,
				)

				toolResults = append(toolResults, provider.ToolResult{
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("BLOCKED: %s", decision.BlockReason),
					IsError:    true,
				})

				// Check if this should trigger escalation
				if agent.Spec.Guardrails.Escalation != nil {
					record.Escalation = &corev1alpha1.ActionEscalation{
						Channel:   string(agent.Spec.Guardrails.Escalation.Target),
						Message:   decision.BlockReason,
						Timestamp: now,
					}
					result.guardrails.EscalationsTriggered++
					metrics.RecordEscalation(agent.Name, decision.BlockReason)
				}

				telemetry.EndToolCallSpan(toolSpan, string(record.Status), true, decision.BlockReason)
			} else {
				// Execute the tool
				toolResult, err := cfg.ToolRegistry.Execute(ctx, tc.Name, tc.Args)
				if err != nil {
					record.Status = corev1alpha1.ActionStatusFailed
					record.Result = security.SanitizeActionResult(fmt.Sprintf("execution error: %v", err), 4096)

					toolResults = append(toolResults, provider.ToolResult{
						ToolCallID: tc.ID,
						Content:    fmt.Sprintf("ERROR: %v", err),
						IsError:    true,
					})

					telemetry.EndToolCallSpan(toolSpan, string(corev1alpha1.ActionStatusFailed), false, "")
				} else {
					record.Status = corev1alpha1.ActionStatusExecuted
					// Sanitize + truncate for audit trail (keep full unsanitized for LLM)
					record.Result = security.SanitizeActionResult(toolResult, 4096)

					toolResults = append(toolResults, provider.ToolResult{
						ToolCallID: tc.ID,
						Content:    toolResult,
					})

					// Record execution for cooldown tracking
					if decision.MatchedAction != nil {
						eng.RecordExecution(decision.MatchedAction.ID, target)
					}

					telemetry.EndToolCallSpan(toolSpan, string(corev1alpha1.ActionStatusExecuted), false, "")
				}
			}

			result.actions = append(result.actions, record)
		}

		// Feed tool results back to LLM
		messages = append(messages, provider.Message{
			Role:        "user",
			ToolResults: toolResults,
		})

		// Conversation pruning: keep the first message (task instruction) plus
		// the last maxConversationPairs exchanges to prevent quadratic context growth.
		// Each "pair" is (assistant + user) = 2 messages.
		messages = pruneConversation(messages, maxConversationPairs)
	}

	// Check if we exhausted iterations
	if result.iterations >= maxIterations && result.phase == corev1alpha1.RunPhaseSucceeded && result.report == "" {
		result.phase = corev1alpha1.RunPhaseFailed
		result.report = fmt.Sprintf("max iterations exhausted (%d)", maxIterations)
	}

	// If any escalation was triggered, mark as Escalated
	if result.guardrails.EscalationsTriggered > 0 && result.phase == corev1alpha1.RunPhaseSucceeded {
		result.phase = corev1alpha1.RunPhaseEscalated
	}

	// If all tool calls were blocked, mark as Blocked
	if result.guardrails.ActionsBlocked > 0 && len(result.actions) > 0 {
		allBlocked := true
		for _, a := range result.actions {
			if a.Status == corev1alpha1.ActionStatusExecuted {
				allBlocked = false
				break
			}
		}
		if allBlocked {
			result.phase = corev1alpha1.RunPhaseBlocked
		}
	}

	return result
}

func (r *Runner) createLegatorRun(
	agent *corev1alpha1.LegatorAgent,
	assembled *assembler.AssembledAgent,
	trigger corev1alpha1.RunTrigger,
) *corev1alpha1.LegatorRun {
	return &corev1alpha1.LegatorRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: agent.Name + "-",
			Namespace:    agent.Namespace,
			Labels: map[string]string{
				"legator.io/agent": agent.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: corev1alpha1.GroupVersion.String(),
					Kind:       "LegatorAgent",
					Name:       agent.Name,
					UID:        agent.UID,
				},
			},
		},
		Spec: corev1alpha1.LegatorRunSpec{
			AgentRef:       agent.Name,
			EnvironmentRef: agent.Spec.EnvironmentRef,
			Trigger:        trigger,
			ModelUsed:      assembled.Model.FullModelString,
		},
		Status: corev1alpha1.LegatorRunStatus{
			Phase: corev1alpha1.RunPhasePending,
		},
	}
}

func (r *Runner) createFailedRun(
	agent *corev1alpha1.LegatorAgent,
	trigger corev1alpha1.RunTrigger,
	startTime time.Time,
	reason string,
) *corev1alpha1.LegatorRun {
	now := metav1.Now()
	wallClock := time.Since(startTime).Milliseconds()

	return &corev1alpha1.LegatorRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: agent.Name + "-",
			Namespace:    agent.Namespace,
			Labels: map[string]string{
				"legator.io/agent": agent.Name,
			},
		},
		Spec: corev1alpha1.LegatorRunSpec{
			AgentRef:       agent.Name,
			EnvironmentRef: agent.Spec.EnvironmentRef,
			Trigger:        trigger,
		},
		Status: corev1alpha1.LegatorRunStatus{
			Phase:          corev1alpha1.RunPhaseFailed,
			StartTime:      &metav1.Time{Time: startTime},
			CompletionTime: &now,
			Report:         reason,
			Usage: &corev1alpha1.UsageSummary{
				WallClockMs: wallClock,
			},
		},
	}
}

func (r *Runner) finalizeRun(
	ctx context.Context,
	run *corev1alpha1.LegatorRun,
	result *conversationResult,
	startTime time.Time,
	agent *corev1alpha1.LegatorAgent,
	assembled *assembler.AssembledAgent,
) {
	now := metav1.Now()
	wallClock := time.Since(startTime).Milliseconds()

	run.Status.Phase = result.phase
	run.Status.CompletionTime = &now
	run.Status.Actions = result.actions
	run.Status.Findings = result.findings
	run.Status.Report = result.report

	run.Status.Usage = &corev1alpha1.UsageSummary{
		TokensIn:    result.totalIn,
		TokensOut:   result.totalOut,
		TotalTokens: result.totalIn + result.totalOut,
		Iterations:  result.iterations,
		WallClockMs: wallClock,
	}

	result.guardrails.BudgetUsed = r.buildBudgetUsage(result, agent.Spec.Model.TokenBudget, agent.Spec.Guardrails.MaxIterations, agent)
	run.Status.Guardrails = &result.guardrails

	// Set conditions
	condition := metav1.Condition{
		Type:               "Complete",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             string(result.phase),
		Message:            result.report,
	}
	if len(condition.Message) > 256 {
		condition.Message = condition.Message[:256]
	}
	run.Status.Conditions = []metav1.Condition{condition}

	// Update LegatorRun status (terminal — no more modifications after this)
	if err := r.client.Status().Update(ctx, run); err != nil {
		r.log.Error(err, "failed to finalize LegatorRun",
			"agentRun", run.Name,
			"phase", result.phase,
		)
	}

	// Metrics: record run completion
	modelUsed := ""
	if assembled != nil {
		modelUsed = assembled.Model.FullModelString
	}
	metrics.RecordRunComplete(
		agent.Name,
		string(result.phase),
		modelUsed,
		time.Duration(wallClock)*time.Millisecond,
		result.totalIn,
		result.totalOut,
		result.iterations,
	)

	// Metrics: record findings
	for _, f := range result.findings {
		metrics.RecordFinding(agent.Name, string(f.Severity))
	}

	r.log.Info("agent run completed",
		"agent", agent.Name,
		"run", run.Name,
		"phase", result.phase,
		"iterations", result.iterations,
		"tokens", result.totalIn+result.totalOut,
		"actions", len(result.actions),
		"blocked", result.guardrails.ActionsBlocked,
		"wallClockMs", wallClock,
	)
}

func (r *Runner) buildBudgetUsage(
	result *conversationResult,
	tokenBudget int64,
	maxIterations int32,
	agent *corev1alpha1.LegatorAgent,
) *corev1alpha1.BudgetUsage {
	timeout, _ := time.ParseDuration(agent.Spec.Model.Timeout)

	return &corev1alpha1.BudgetUsage{
		TokensUsed:     result.totalIn + result.totalOut,
		TokenBudget:    tokenBudget,
		IterationsUsed: result.iterations,
		MaxIterations:  maxIterations,
		TimeoutMs:      timeout.Milliseconds(),
	}
}

// maxConversationPairs is the number of recent (assistant+user) exchange pairs
// to keep in the conversation history. Earlier exchanges are pruned to prevent
// quadratic context growth. The first message (task instruction) is always kept.
const maxConversationPairs = 4

// pruneConversation keeps the first message and the last N pairs of messages.
// This prevents the conversation from growing without bound as tool calls accumulate.
func pruneConversation(messages []provider.Message, keepPairs int) []provider.Message {
	keepMessages := keepPairs * 2 // each pair = assistant + user
	// +1 for the initial task instruction message
	maxLen := keepMessages + 1
	if len(messages) <= maxLen {
		return messages
	}
	// Keep first message + last keepMessages
	pruned := make([]provider.Message, 0, maxLen)
	pruned = append(pruned, messages[0])
	pruned = append(pruned, messages[len(messages)-keepMessages:]...)
	return pruned
}

// capMaxTokens ensures max_tokens doesn't exceed model-level API limits.
// Anthropic Sonnet: 64K, Opus: 32K, Haiku: 8K output.
// We use a conservative 8192 per-call cap — agents iterate, they don't monologue.
func capMaxTokens(remaining int32) int32 {
	const perCallCap int32 = 8192
	if remaining <= 0 {
		return perCallCap
	}
	if remaining > perCallCap {
		return perCallCap
	}
	return remaining
}

// extractFindings parses agent output for structured findings.
// Looks for patterns like "FINDING: severity: message" or "WARNING: message"
func extractFindings(content string) []corev1alpha1.RunFinding {
	var findings []corev1alpha1.RunFinding

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "CRITICAL:") || strings.HasPrefix(line, "🔴") {
			findings = append(findings, corev1alpha1.RunFinding{
				Severity: corev1alpha1.FindingSeverityCritical,
				Message:  strings.TrimPrefix(strings.TrimPrefix(line, "CRITICAL:"), "🔴"),
			})
		} else if strings.HasPrefix(line, "WARNING:") || strings.HasPrefix(line, "⚠️") || strings.HasPrefix(line, "🟡") {
			findings = append(findings, corev1alpha1.RunFinding{
				Severity: corev1alpha1.FindingSeverityWarning,
				Message:  strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(line, "WARNING:"), "⚠️"), "🟡"),
			})
		} else if strings.HasPrefix(line, "INFO:") || strings.HasPrefix(line, "ℹ️") || strings.HasPrefix(line, "🔵") {
			findings = append(findings, corev1alpha1.RunFinding{
				Severity: corev1alpha1.FindingSeverityInfo,
				Message:  strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(line, "INFO:"), "ℹ️"), "🔵"),
			})
		}
	}

	return findings
}
