/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package a2a

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

func newTestRouter() *Router {
	scheme := runtime.NewScheme()
	corev1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	return NewRouter(client, "agents")
}

func TestDelegateTask(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	id, err := r.DelegateTask(ctx, TaskRequest{
		FromAgent:   "watchman-light",
		ToAgent:     "tribune",
		TaskType:    "investigate",
		Description: "Pod backstage-dev-0 has been crashing for 3 cycles",
		Priority:    PriorityHigh,
	})

	if err != nil {
		t.Fatalf("DelegateTask error: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty task ID")
	}
}

func TestDelegateTask_MissingFields(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	_, err := r.DelegateTask(ctx, TaskRequest{FromAgent: "a"})
	if err == nil {
		t.Error("expected error for missing ToAgent")
	}

	_, err = r.DelegateTask(ctx, TaskRequest{FromAgent: "a", ToAgent: "b"})
	if err == nil {
		t.Error("expected error for missing description")
	}
}

func TestDelegateTask_DefaultPriority(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	id, _ := r.DelegateTask(ctx, TaskRequest{
		FromAgent:   "watchman",
		ToAgent:     "forge",
		Description: "Deploy fix",
	})

	task, err := r.GetTaskResult(ctx, id)
	if err != nil {
		t.Fatalf("GetTaskResult: %v", err)
	}
	if task.Priority != PriorityNormal {
		t.Errorf("priority = %q, want normal", task.Priority)
	}
}

func TestGetPendingTasks(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	// Create two tasks for tribune
	r.DelegateTask(ctx, TaskRequest{
		FromAgent: "watchman", ToAgent: "tribune",
		TaskType: "investigate", Description: "Check pod crash",
		Priority: PriorityNormal,
	})
	r.DelegateTask(ctx, TaskRequest{
		FromAgent: "scout", ToAgent: "tribune",
		TaskType: "report", Description: "Broken dashboard link",
		Priority: PriorityHigh,
	})
	// Create one task for forge (should not appear)
	r.DelegateTask(ctx, TaskRequest{
		FromAgent: "tribune", ToAgent: "forge",
		Description: "Deploy hotfix",
	})

	tasks, err := r.GetPendingTasks(ctx, "tribune")
	if err != nil {
		t.Fatalf("GetPendingTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks for tribune, got %d", len(tasks))
	}

	// High priority should be first
	if tasks[0].Priority != PriorityHigh {
		t.Error("expected high priority task first")
	}
}

func TestCompleteTask(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	id, _ := r.DelegateTask(ctx, TaskRequest{
		FromAgent: "watchman", ToAgent: "tribune",
		Description: "Check pod crash",
	})

	err := r.CompleteTask(ctx, id, "Pod recovered after OOM kill. Memory limit bumped.")
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	task, _ := r.GetTaskResult(ctx, id)
	if task.Status != TaskStatusCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.Result != "Pod recovered after OOM kill. Memory limit bumped." {
		t.Errorf("result = %q", task.Result)
	}
}

func TestFailTask(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	id, _ := r.DelegateTask(ctx, TaskRequest{
		FromAgent: "watchman", ToAgent: "forge",
		Description: "Deploy fix",
	})

	r.FailTask(ctx, id, "Build failed: compilation error")

	task, _ := r.GetTaskResult(ctx, id)
	if task.Status != TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
}

func TestRejectTask(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	id, _ := r.DelegateTask(ctx, TaskRequest{
		FromAgent: "watchman", ToAgent: "analyst",
		Description: "Run deep analysis",
	})

	r.RejectTask(ctx, id, "Not in my scope")

	task, _ := r.GetTaskResult(ctx, id)
	if task.Status != TaskStatusRejected {
		t.Errorf("status = %q, want rejected", task.Status)
	}
}

func TestGetDelegatedTasks(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	r.DelegateTask(ctx, TaskRequest{
		FromAgent: "watchman", ToAgent: "tribune",
		Description: "Task 1",
	})
	r.DelegateTask(ctx, TaskRequest{
		FromAgent: "watchman", ToAgent: "forge",
		Description: "Task 2",
	})
	r.DelegateTask(ctx, TaskRequest{
		FromAgent: "scout", ToAgent: "tribune",
		Description: "Task 3",
	})

	tasks, err := r.GetDelegatedTasks(ctx, "watchman")
	if err != nil {
		t.Fatalf("GetDelegatedTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 delegated tasks from watchman, got %d", len(tasks))
	}
}

func TestExpireOldTasks(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1alpha1.AddToScheme(scheme)

	// Create an old event manually
	oldEvent := &corev1alpha1.AgentEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "a2a-old-task",
			Namespace: "agents",
			Labels: map[string]string{
				"legator.io/event-type":  "a2a-task",
				"legator.io/task-status": string(TaskStatusPending),
			},
			// CreationTimestamp will be set by the fake client (effectively zero)
		},
		Spec: corev1alpha1.AgentEventSpec{
			EventType:   "a2a.task.investigate",
			SourceAgent: "watchman",
			Severity:    corev1alpha1.EventSeverityInfo,
			Summary:     "Old task",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oldEvent).
		Build()

	r := NewRouter(c, "agents")
	ctx := context.Background()

	// With zero CreationTimestamp, the task appears "very old"
	// maxAge of 0 means "expire everything"
	// But the fake client sets CreationTimestamp to zero, which is before any cutoff
	expired, err := r.ExpireOldTasks(ctx, 0)
	if err != nil {
		t.Fatalf("ExpireOldTasks: %v", err)
	}
	// Zero timestamp is epoch, always before cutoff, so should expire
	if expired != 1 {
		t.Errorf("expired = %d, want 1", expired)
	}
}

func TestDelegateTaskTool(t *testing.T) {
	r := newTestRouter()
	tool := NewDelegateTaskTool(r, "watchman")

	if tool.Name() != "a2a.delegate" {
		t.Errorf("name = %q, want a2a.delegate", tool.Name())
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"target_agent": "tribune",
		"task_type":    "investigate",
		"description":  "Pod crashing in backstage-dev",
		"priority":     "high",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestCheckTasksTool_NoPending(t *testing.T) {
	r := newTestRouter()
	tool := NewCheckTasksTool(r, "tribune")

	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "No pending tasks." {
		t.Errorf("result = %q, want 'No pending tasks.'", result)
	}
}

func TestCheckTasksTool_WithPending(t *testing.T) {
	r := newTestRouter()
	ctx := context.Background()

	r.DelegateTask(ctx, TaskRequest{
		FromAgent: "watchman", ToAgent: "tribune",
		Description: "Check pod crash",
	})

	tool := NewCheckTasksTool(r, "tribune")
	result, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == "No pending tasks." {
		t.Error("expected pending tasks")
	}
}

func TestPriorityOrder(t *testing.T) {
	if priorityOrder(PriorityCritical) <= priorityOrder(PriorityHigh) {
		t.Error("critical should outrank high")
	}
	if priorityOrder(PriorityHigh) <= priorityOrder(PriorityNormal) {
		t.Error("high should outrank normal")
	}
	if priorityOrder(PriorityNormal) <= priorityOrder(PriorityLow) {
		t.Error("normal should outrank low")
	}
}

func TestSanitizeName(t *testing.T) {
	if s := sanitizeName("Watchman Light"); s != "watchman-light" {
		t.Errorf("sanitize = %q", s)
	}
	long := "this-is-a-very-long-agent-name-that-exceeds-thirty-characters"
	if s := sanitizeName(long); len(s) > 30 {
		t.Errorf("sanitize too long: %d", len(s))
	}
}
