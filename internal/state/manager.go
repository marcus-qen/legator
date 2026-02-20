/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package state provides persistent key-value storage for agents between runs.
// Each agent gets an AgentState CRD with a map of entries. The state tool
// (state.get, state.set, state.delete) allows LLMs to read/write during runs.
// State is injected as context at the start of each run so agents remember
// what they found previously.
package state

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

const (
	// DefaultMaxKeys is the maximum number of keys per agent state.
	DefaultMaxKeys = 100
	// DefaultMaxValueSize is the maximum size of a single value in bytes.
	DefaultMaxValueSize = 4096
	// DefaultMaxTotalSize is the approximate max total size of all entries (64KB).
	DefaultMaxTotalSize = 65536
)

// Manager handles reading and writing agent state.
type Manager struct {
	client client.Client
	log    logr.Logger
}

// NewManager creates a new state manager.
func NewManager(c client.Client, log logr.Logger) *Manager {
	return &Manager{client: c, log: log}
}

// Get retrieves a value from the agent's state.
func (m *Manager) Get(ctx context.Context, agentName, namespace, key string) (string, bool, error) {
	state, err := m.getOrCreate(ctx, agentName, namespace)
	if err != nil {
		return "", false, err
	}

	entry, ok := state.Status.Entries[key]
	if !ok {
		return "", false, nil
	}

	// Check TTL
	if entry.TTL != "" {
		ttl, parseErr := time.ParseDuration(entry.TTL)
		if parseErr == nil && !entry.UpdatedAt.IsZero() && time.Since(entry.UpdatedAt.Time) > ttl {
			// Expired — delete it
			delete(state.Status.Entries, key)
			m.updateStatus(ctx, state)
			return "", false, nil
		}
	}

	return entry.Value, true, nil
}

// Set writes a value to the agent's state.
func (m *Manager) Set(ctx context.Context, agentName, namespace, key, value, runName, ttl string) error {
	state, err := m.getOrCreate(ctx, agentName, namespace)
	if err != nil {
		return err
	}

	// Check quotas
	maxKeys := state.Spec.MaxKeys
	if maxKeys == 0 {
		maxKeys = DefaultMaxKeys
	}
	maxValueSize := state.Spec.MaxValueSize
	if maxValueSize == 0 {
		maxValueSize = DefaultMaxValueSize
	}

	if len(value) > maxValueSize {
		return fmt.Errorf("value size %d exceeds max %d bytes", len(value), maxValueSize)
	}

	if state.Status.Entries == nil {
		state.Status.Entries = make(map[string]corev1alpha1.StateEntry)
	}

	// Check key count (only for new keys)
	if _, exists := state.Status.Entries[key]; !exists && len(state.Status.Entries) >= maxKeys {
		return fmt.Errorf("max keys (%d) exceeded", maxKeys)
	}

	state.Status.Entries[key] = corev1alpha1.StateEntry{
		Value:     value,
		UpdatedAt: metav1.Now(),
		UpdatedBy: runName,
		TTL:       ttl,
	}

	// Recalculate total size
	totalSize := 0
	for k, v := range state.Status.Entries {
		totalSize += len(k) + len(v.Value)
	}

	if totalSize > DefaultMaxTotalSize {
		return fmt.Errorf("total state size %d exceeds max %d bytes", totalSize, DefaultMaxTotalSize)
	}

	state.Status.TotalSize = totalSize
	state.Status.LastUpdated = metav1.Now()

	return m.updateStatus(ctx, state)
}

// Delete removes a key from the agent's state.
func (m *Manager) Delete(ctx context.Context, agentName, namespace, key string) error {
	state, err := m.getOrCreate(ctx, agentName, namespace)
	if err != nil {
		return err
	}

	if state.Status.Entries == nil {
		return nil
	}

	delete(state.Status.Entries, key)

	// Recalculate total size
	totalSize := 0
	for k, v := range state.Status.Entries {
		totalSize += len(k) + len(v.Value)
	}
	state.Status.TotalSize = totalSize
	state.Status.LastUpdated = metav1.Now()

	return m.updateStatus(ctx, state)
}

// ListKeys returns all keys in the agent's state (for context injection).
func (m *Manager) ListKeys(ctx context.Context, agentName, namespace string) (map[string]corev1alpha1.StateEntry, error) {
	state, err := m.getOrCreate(ctx, agentName, namespace)
	if err != nil {
		return nil, err
	}

	// Clean expired entries
	cleaned := false
	for key, entry := range state.Status.Entries {
		if entry.TTL != "" {
			ttl, parseErr := time.ParseDuration(entry.TTL)
			if parseErr == nil && !entry.UpdatedAt.IsZero() && time.Since(entry.UpdatedAt.Time) > ttl {
				delete(state.Status.Entries, key)
				cleaned = true
			}
		}
	}
	if cleaned {
		m.updateStatus(ctx, state)
	}

	return state.Status.Entries, nil
}

// FormatContext produces a human-readable summary of agent state for prompt injection.
func (m *Manager) FormatContext(ctx context.Context, agentName, namespace string) (string, error) {
	entries, err := m.ListKeys(ctx, agentName, namespace)
	if err != nil {
		return "", err
	}

	if len(entries) == 0 {
		return "", nil
	}

	result := "## Previous State (from your last run)\n\n"
	for key, entry := range entries {
		result += fmt.Sprintf("### %s\n", key)
		result += fmt.Sprintf("*Updated: %s by %s*\n", entry.UpdatedAt.Format(time.RFC3339), entry.UpdatedBy)
		result += entry.Value + "\n\n"
	}

	return result, nil
}

// getOrCreate fetches the AgentState, creating it if it doesn't exist.
func (m *Manager) getOrCreate(ctx context.Context, agentName, namespace string) (*corev1alpha1.AgentState, error) {
	stateName := agentName + "-state"

	state := &corev1alpha1.AgentState{}
	err := m.client.Get(ctx, client.ObjectKey{Name: stateName, Namespace: namespace}, state)
	if err == nil {
		return state, nil
	}

	// Create if not found
	state = &corev1alpha1.AgentState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stateName,
			Namespace: namespace,
			Labels: map[string]string{
				"legator.io/agent": agentName,
			},
		},
		Spec: corev1alpha1.AgentStateSpec{
			AgentName:    agentName,
			MaxKeys:      DefaultMaxKeys,
			MaxValueSize: DefaultMaxValueSize,
		},
	}

	if err := m.client.Create(ctx, state); err != nil {
		return nil, fmt.Errorf("create AgentState: %w", err)
	}

	m.log.Info("created AgentState", "agent", agentName, "name", stateName)
	return state, nil
}

func (m *Manager) updateStatus(ctx context.Context, state *corev1alpha1.AgentState) error {
	if err := m.client.Status().Update(ctx, state); err != nil {
		return fmt.Errorf("update AgentState status: %w", err)
	}
	return nil
}
