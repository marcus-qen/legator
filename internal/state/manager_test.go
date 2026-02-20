/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package state

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	corev1alpha1.AddToScheme(s)
	return s
}

func TestManager_SetAndGet(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	// Set a value
	err := m.Set(ctx, "watchman", "agents", "last-finding", `{"pod":"backstage","issue":"CrashLoopBackOff"}`, "watchman-run-1", "")
	if err != nil {
		t.Fatalf("Set error: %v", err)
	}

	// Get it back
	value, found, err := m.Get(ctx, "watchman", "agents", "last-finding")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if !found {
		t.Fatal("expected key to be found")
	}
	if !strings.Contains(value, "CrashLoopBackOff") {
		t.Errorf("value = %q, expected to contain CrashLoopBackOff", value)
	}
}

func TestManager_GetNotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	_, found, err := m.Get(ctx, "watchman", "agents", "nonexistent")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if found {
		t.Error("expected key not to be found")
	}
}

func TestManager_Delete(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	// Set then delete
	m.Set(ctx, "watchman", "agents", "temp-key", "temp-value", "run-1", "")
	err := m.Delete(ctx, "watchman", "agents", "temp-key")
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	// Verify gone
	_, found, _ := m.Get(ctx, "watchman", "agents", "temp-key")
	if found {
		t.Error("expected key to be deleted")
	}
}

func TestManager_MaxValueSize(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	// Try to set a value larger than default max (4096)
	bigValue := strings.Repeat("x", DefaultMaxValueSize+1)
	err := m.Set(ctx, "watchman", "agents", "big-key", bigValue, "run-1", "")
	if err == nil {
		t.Error("expected error for oversized value")
	}
}

func TestManager_MaxKeys(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	// Fill to max keys (using a small max)
	// First create state with small max
	m.Set(ctx, "watchman", "agents", "key-0", "v", "run-1", "")

	// Manually update the max keys to a small number
	state := &corev1alpha1.AgentState{}
	c.Get(ctx, client.ObjectKey{Name: "watchman-state", Namespace: "agents"}, state)
	state.Spec.MaxKeys = 2
	c.Update(ctx, state)

	// Second key should work
	err := m.Set(ctx, "watchman", "agents", "key-1", "v", "run-1", "")
	if err != nil {
		t.Fatalf("Set key-1 error: %v", err)
	}

	// Third key should fail
	err = m.Set(ctx, "watchman", "agents", "key-2", "v", "run-1", "")
	if err == nil {
		t.Error("expected error for exceeding max keys")
	}
}

func TestManager_UpdateExistingKey(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	// Set a key
	m.Set(ctx, "watchman", "agents", "finding", "old-value", "run-1", "")

	// Overwrite it
	m.Set(ctx, "watchman", "agents", "finding", "new-value", "run-2", "")

	// Should get new value
	value, found, _ := m.Get(ctx, "watchman", "agents", "finding")
	if !found || value != "new-value" {
		t.Errorf("expected new-value, got %q (found=%v)", value, found)
	}
}

func TestManager_FormatContext(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	// Empty state
	text, err := m.FormatContext(ctx, "watchman", "agents")
	if err != nil {
		t.Fatalf("FormatContext error: %v", err)
	}
	if text != "" {
		t.Error("expected empty context for no state")
	}

	// Add some state
	m.Set(ctx, "watchman", "agents", "last-findings", "3 pods crashing", "run-1", "")
	m.Set(ctx, "watchman", "agents", "known-issues", "#155 cilium netpol", "run-1", "")

	text, err = m.FormatContext(ctx, "watchman", "agents")
	if err != nil {
		t.Fatalf("FormatContext error: %v", err)
	}
	if !strings.Contains(text, "Previous State") {
		t.Error("expected header in context")
	}
	if !strings.Contains(text, "3 pods crashing") {
		t.Error("expected findings in context")
	}
}

func TestManager_ListKeys(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(&corev1alpha1.AgentState{}).Build()
	m := NewManager(c, logr.Discard())
	ctx := context.Background()

	m.Set(ctx, "watchman", "agents", "key-a", "value-a", "run-1", "")
	m.Set(ctx, "watchman", "agents", "key-b", "value-b", "run-1", "")

	entries, err := m.ListKeys(ctx, "watchman", "agents")
	if err != nil {
		t.Fatalf("ListKeys error: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}
