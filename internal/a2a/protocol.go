/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package a2a provides Agent-to-Agent (A2A) communication.
// Agents delegate tasks to other agents via CRD-based task requests.
// This is NOT the Google A2A protocol — it's a simpler CRD-native approach
// that fits our existing K8s-native architecture.
//
// Flow:
//  1. Agent A creates a TaskRequest CRD targeting Agent B
//  2. Agent B's next run discovers the pending task
//  3. Agent B processes the task and updates the result
//  4. Agent A discovers the completed result on its next run
//
// This is intentionally CRD-based (not HTTP-based) because:
//  - Agents already have K8s API access
//  - Built-in persistence, RBAC, watch/list
//  - No additional networking or service mesh needed
//  - Audit trail via standard K8s events
package a2a

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/tools"
)

// TaskPriority defines urgency of a task request.
type TaskPriority string

const (
	PriorityLow      TaskPriority = "low"
	PriorityNormal   TaskPriority = "normal"
	PriorityHigh     TaskPriority = "high"
	PriorityCritical TaskPriority = "critical"
)

// TaskStatus is the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusAccepted   TaskStatus = "accepted"
	TaskStatusInProgress TaskStatus = "in-progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusRejected   TaskStatus = "rejected"
	TaskStatusExpired    TaskStatus = "expired"
)

// TaskRequest represents a task delegated between agents.
// Stored as AgentEvent CRDs with specific labels for A2A filtering.
type TaskRequest struct {
	// ID is the unique task identifier (AgentEvent name).
	ID string

	// FromAgent is the requesting agent.
	FromAgent string

	// ToAgent is the target agent.
	ToAgent string

	// TaskType categorises the task (e.g., "investigate", "remediate", "report").
	TaskType string

	// Description is the human/LLM-readable task description.
	Description string

	// Priority indicates urgency.
	Priority TaskPriority

	// Context provides additional data for the target agent.
	Context map[string]string

	// Status is the current task state.
	Status TaskStatus

	// Result is the completion output (set by target agent).
	Result string

	// CreatedAt is when the task was created.
	CreatedAt time.Time

	// ExpiresAt is the deadline (optional).
	ExpiresAt *time.Time

	// CompletedAt is when the task was finished.
	CompletedAt *time.Time
}

// Router manages A2A task delegation between agents.
type Router struct {
	client    client.Client
	namespace string
}

// NewRouter creates an A2A task router.
func NewRouter(c client.Client, namespace string) *Router {
	return &Router{client: c, namespace: namespace}
}

// DelegateTask creates a task request from one agent to another.
func (r *Router) DelegateTask(ctx context.Context, task TaskRequest) (string, error) {
	if task.FromAgent == "" || task.ToAgent == "" {
		return "", fmt.Errorf("both from and to agents are required")
	}
	if task.Description == "" {
		return "", fmt.Errorf("task description is required")
	}
	if task.Priority == "" {
		task.Priority = PriorityNormal
	}
	if task.TaskType == "" {
		task.TaskType = "general"
	}

	// Create as AgentEvent with A2A labels
	name := fmt.Sprintf("a2a-%s-to-%s-%d", sanitizeName(task.FromAgent), sanitizeName(task.ToAgent), time.Now().UnixMilli())

	severity := "info"
	if task.Priority == PriorityHigh || task.Priority == PriorityCritical {
		severity = "warning"
	}

	// Build detail string with context
	detail := task.Description
	if len(task.Context) > 0 {
		var parts []string
		for k, v := range task.Context {
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		}
		sort.Strings(parts)
		detail += "\n\nContext:\n" + strings.Join(parts, "\n")
	}

	event := &corev1alpha1.AgentEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.namespace,
			Labels: map[string]string{
				"legator.io/event-type":   "a2a-task",
				"legator.io/source-agent": task.FromAgent,
				"legator.io/target-agent": task.ToAgent,
				"legator.io/task-type":    task.TaskType,
				"legator.io/priority":     string(task.Priority),
				"legator.io/task-status":  string(TaskStatusPending),
			},
		},
		Spec: corev1alpha1.AgentEventSpec{
			EventType:   "a2a.task." + task.TaskType,
			SourceAgent: task.FromAgent,
			Severity:    corev1alpha1.AgentEventSeverity(severity),
			Summary:     fmt.Sprintf("[A2A] %s → %s: %s", task.FromAgent, task.ToAgent, truncate(task.Description, 100)),
			Detail:      detail,
			TargetAgent: task.ToAgent,
		},
	}

	if err := r.client.Create(ctx, event); err != nil {
		return "", fmt.Errorf("create task event: %w", err)
	}

	return name, nil
}

// GetPendingTasks returns tasks assigned to an agent that haven't been processed.
func (r *Router) GetPendingTasks(ctx context.Context, agentName string) ([]TaskRequest, error) {
	list := &corev1alpha1.AgentEventList{}
	selector := labels.SelectorFromSet(map[string]string{
		"legator.io/event-type":   "a2a-task",
		"legator.io/target-agent": agentName,
		"legator.io/task-status":  string(TaskStatusPending),
	})

	if err := r.client.List(ctx, list, &client.ListOptions{
		Namespace:     r.namespace,
		LabelSelector: selector,
	}); err != nil {
		return nil, fmt.Errorf("list pending tasks: %w", err)
	}

	var tasks []TaskRequest
	for _, event := range list.Items {
		tasks = append(tasks, eventToTask(event))
	}

	// Sort by priority (critical first) then by age (oldest first)
	sort.Slice(tasks, func(i, j int) bool {
		pi, pj := priorityOrder(tasks[i].Priority), priorityOrder(tasks[j].Priority)
		if pi != pj {
			return pi > pj
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	return tasks, nil
}

// CompleteTask marks a task as completed with a result.
func (r *Router) CompleteTask(ctx context.Context, taskID string, result string) error {
	return r.updateTaskStatus(ctx, taskID, TaskStatusCompleted, result)
}

// FailTask marks a task as failed with an error message.
func (r *Router) FailTask(ctx context.Context, taskID string, reason string) error {
	return r.updateTaskStatus(ctx, taskID, TaskStatusFailed, reason)
}

// RejectTask marks a task as rejected.
func (r *Router) RejectTask(ctx context.Context, taskID string, reason string) error {
	return r.updateTaskStatus(ctx, taskID, TaskStatusRejected, reason)
}

// AcceptTask marks a task as accepted (in progress).
func (r *Router) AcceptTask(ctx context.Context, taskID string) error {
	return r.updateTaskStatus(ctx, taskID, TaskStatusAccepted, "")
}

// GetTaskResult checks if a delegated task has been completed.
func (r *Router) GetTaskResult(ctx context.Context, taskID string) (*TaskRequest, error) {
	event := &corev1alpha1.AgentEvent{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: taskID, Namespace: r.namespace}, event); err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}

	task := eventToTask(*event)
	return &task, nil
}

// GetDelegatedTasks returns tasks created by an agent (outbound).
func (r *Router) GetDelegatedTasks(ctx context.Context, agentName string) ([]TaskRequest, error) {
	list := &corev1alpha1.AgentEventList{}
	selector := labels.SelectorFromSet(map[string]string{
		"legator.io/event-type":   "a2a-task",
		"legator.io/source-agent": agentName,
	})

	if err := r.client.List(ctx, list, &client.ListOptions{
		Namespace:     r.namespace,
		LabelSelector: selector,
	}); err != nil {
		return nil, fmt.Errorf("list delegated tasks: %w", err)
	}

	var tasks []TaskRequest
	for _, event := range list.Items {
		tasks = append(tasks, eventToTask(event))
	}

	return tasks, nil
}

// ExpireOldTasks marks stale pending tasks as expired.
func (r *Router) ExpireOldTasks(ctx context.Context, maxAge time.Duration) (int, error) {
	list := &corev1alpha1.AgentEventList{}
	selector := labels.SelectorFromSet(map[string]string{
		"legator.io/event-type":  "a2a-task",
		"legator.io/task-status": string(TaskStatusPending),
	})

	if err := r.client.List(ctx, list, &client.ListOptions{
		Namespace:     r.namespace,
		LabelSelector: selector,
	}); err != nil {
		return 0, fmt.Errorf("list tasks: %w", err)
	}

	expired := 0
	cutoff := time.Now().Add(-maxAge)
	for _, event := range list.Items {
		if event.CreationTimestamp.Time.Before(cutoff) {
			event.Labels["legator.io/task-status"] = string(TaskStatusExpired)
			if err := r.client.Update(ctx, &event); err != nil {
				continue
			}
			expired++
		}
	}

	return expired, nil
}

// --- A2A Tools (for LLM use) ---

// DelegateTaskTool implements Tool for LLM-driven task delegation.
type DelegateTaskTool struct {
	router    *Router
	agentName string
}

// NewDelegateTaskTool creates the a2a.delegate tool for an agent.
func NewDelegateTaskTool(router *Router, agentName string) *DelegateTaskTool {
	return &DelegateTaskTool{router: router, agentName: agentName}
}

func (t *DelegateTaskTool) Name() string { return "a2a.delegate" }

func (t *DelegateTaskTool) Description() string {
	return "Delegate a task to another agent. The target agent will process it on their next run."
}

func (t *DelegateTaskTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target_agent": map[string]interface{}{
				"type":        "string",
				"description": "Name of the agent to delegate to",
			},
			"task_type": map[string]interface{}{
				"type":        "string",
				"description": "Type of task (investigate, remediate, report, escalate)",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "What you want the target agent to do",
			},
			"priority": map[string]interface{}{
				"type":        "string",
				"description": "Task priority (low, normal, high, critical)",
				"default":     "normal",
			},
		},
		"required": []string{"target_agent", "description"},
	}
}

func (t *DelegateTaskTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	target, _ := args["target_agent"].(string)
	taskType, _ := args["task_type"].(string)
	desc, _ := args["description"].(string)
	priority, _ := args["priority"].(string)

	task := TaskRequest{
		FromAgent:   t.agentName,
		ToAgent:     target,
		TaskType:    taskType,
		Description: desc,
		Priority:    TaskPriority(priority),
	}

	id, err := t.router.DelegateTask(ctx, task)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Task delegated to %s: %s (ID: %s)", target, truncate(desc, 80), id), nil
}

// Capability implements ClassifiableTool. Delegation is internal bookkeeping (creating
// a CRD event), not an external system mutation — safe at observe level.
func (t *DelegateTaskTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{
		Domain:         "a2a",
		SupportedTiers: []tools.ActionTier{tools.TierRead},
	}
}

// ClassifyAction implements ClassifiableTool.
func (t *DelegateTaskTool) ClassifyAction(args map[string]interface{}) tools.ActionClassification {
	target, _ := args["target_agent"].(string)
	return tools.ActionClassification{
		Tier:        tools.TierRead,
		Description: fmt.Sprintf("delegate task to agent %q (internal coordination)", target),
	}
}

// CheckTasksTool implements Tool for checking incoming tasks.
type CheckTasksTool struct {
	router    *Router
	agentName string
}

// NewCheckTasksTool creates the a2a.check_tasks tool for an agent.
func NewCheckTasksTool(router *Router, agentName string) *CheckTasksTool {
	return &CheckTasksTool{router: router, agentName: agentName}
}

func (t *CheckTasksTool) Name() string { return "a2a.check_tasks" }

func (t *CheckTasksTool) Description() string {
	return "Check for pending tasks delegated to you by other agents."
}

func (t *CheckTasksTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *CheckTasksTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	tasks, err := t.router.GetPendingTasks(ctx, t.agentName)
	if err != nil {
		return "", err
	}

	if len(tasks) == 0 {
		return "No pending tasks.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d pending task(s):\n\n", len(tasks))
	for _, task := range tasks {
		fmt.Fprintf(&sb, "- [%s] %s from %s: %s (ID: %s)\n",
			task.Priority, task.TaskType, task.FromAgent, truncate(task.Description, 100), task.ID)
	}

	return sb.String(), nil
}

// Capability implements ClassifiableTool. Checking tasks is purely read-only.
func (t *CheckTasksTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{
		Domain:         "a2a",
		SupportedTiers: []tools.ActionTier{tools.TierRead},
	}
}

// ClassifyAction implements ClassifiableTool.
func (t *CheckTasksTool) ClassifyAction(args map[string]interface{}) tools.ActionClassification {
	return tools.ActionClassification{
		Tier:        tools.TierRead,
		Description: "check pending tasks from other agents (read-only)",
	}
}

// --- Helpers ---

func (r *Router) updateTaskStatus(ctx context.Context, taskID string, status TaskStatus, result string) error {
	event := &corev1alpha1.AgentEvent{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: taskID, Namespace: r.namespace}, event); err != nil {
		return fmt.Errorf("get task: %w", err)
	}

	event.Labels["legator.io/task-status"] = string(status)
	if result != "" {
		if event.Annotations == nil {
			event.Annotations = make(map[string]string)
		}
		event.Annotations["legator.io/task-result"] = result
	}

	return r.client.Update(ctx, event)
}

func eventToTask(event corev1alpha1.AgentEvent) TaskRequest {
	task := TaskRequest{
		ID:          event.Name,
		FromAgent:   event.Labels["legator.io/source-agent"],
		ToAgent:     event.Labels["legator.io/target-agent"],
		TaskType:    event.Labels["legator.io/task-type"],
		Priority:    TaskPriority(event.Labels["legator.io/priority"]),
		Status:      TaskStatus(event.Labels["legator.io/task-status"]),
		Description: event.Spec.Detail,
		CreatedAt:   event.CreationTimestamp.Time,
	}

	if event.Annotations != nil {
		task.Result = event.Annotations["legator.io/task-result"]
	}

	return task
}

func priorityOrder(p TaskPriority) int {
	switch p {
	case PriorityCritical:
		return 4
	case PriorityHigh:
		return 3
	case PriorityNormal:
		return 2
	case PriorityLow:
		return 1
	default:
		return 0
	}
}

func sanitizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	if len(name) > 30 {
		name = name[:30]
	}
	return name
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
