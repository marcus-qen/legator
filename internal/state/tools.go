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
	"encoding/json"
	"fmt"

	"github.com/marcus-qen/legator/internal/tools"
)

// StateGetTool reads a value from agent state.
type StateGetTool struct {
	manager   *Manager
	agentName string
	namespace string
}

// NewStateGetTool creates a state.get tool bound to a specific agent.
func NewStateGetTool(manager *Manager, agentName, namespace string) *StateGetTool {
	return &StateGetTool{manager: manager, agentName: agentName, namespace: namespace}
}

func (t *StateGetTool) Name() string        { return "state.get" }
func (t *StateGetTool) Description() string  {
	return "Read a value from your persistent state. Returns the value if found, or indicates the key doesn't exist. Use this to check what you found in previous runs."
}

func (t *StateGetTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"key": map[string]interface{}{
				"type":        "string",
				"description": "The key to read",
			},
		},
		"required": []string{"key"},
	}
}

func (t *StateGetTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return "", fmt.Errorf("key is required")
	}

	value, found, err := t.manager.Get(ctx, t.agentName, t.namespace, key)
	if err != nil {
		return "", err
	}

	result := map[string]interface{}{
		"key":   key,
		"found": found,
	}
	if found {
		result["value"] = value
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out), nil
}

// Capability implements ClassifiableTool.
func (t *StateGetTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{
		Domain:         "state",
		SupportedTiers: []tools.ActionTier{tools.TierRead},
	}
}

// ClassifyAction implements ClassifiableTool.
func (t *StateGetTool) ClassifyAction(args map[string]interface{}) tools.ActionClassification {
	return tools.ActionClassification{
		Tier:        tools.TierRead,
		Description: "read agent state (read-only)",
	}
}

// --- state.set ---

// StateSetTool writes a value to agent state.
type StateSetTool struct {
	manager   *Manager
	agentName string
	namespace string
	runName   string
}

// NewStateSetTool creates a state.set tool bound to a specific agent and run.
func NewStateSetTool(manager *Manager, agentName, namespace, runName string) *StateSetTool {
	return &StateSetTool{manager: manager, agentName: agentName, namespace: namespace, runName: runName}
}

func (t *StateSetTool) Name() string        { return "state.set" }
func (t *StateSetTool) Description() string  {
	return "Save a value to your persistent state. This persists between runs — use it to remember findings, track issues, and avoid duplicate reports. Values support JSON for structured data."
}

func (t *StateSetTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"key": map[string]interface{}{
				"type":        "string",
				"description": "The key to write (e.g., 'last-findings', 'known-issues')",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "The value to store (plain text or JSON)",
			},
			"ttl": map[string]interface{}{
				"type":        "string",
				"description": "Optional time-to-live (e.g., '24h', '7d'). Entry expires after this duration.",
			},
		},
		"required": []string{"key", "value"},
	}
}

func (t *StateSetTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	ttl, _ := args["ttl"].(string)

	if key == "" {
		return "", fmt.Errorf("key is required")
	}
	if value == "" {
		return "", fmt.Errorf("value is required")
	}

	err := t.manager.Set(ctx, t.agentName, t.namespace, key, value, t.runName, ttl)
	if err != nil {
		return "", err
	}

	result := map[string]interface{}{
		"key":    key,
		"stored": true,
	}
	if ttl != "" {
		result["ttl"] = ttl
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	return string(out), nil
}

// Capability implements ClassifiableTool.
func (t *StateSetTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{
		Domain:         "state",
		SupportedTiers: []tools.ActionTier{tools.TierRead},
	}
}

// ClassifyAction implements ClassifiableTool.
func (t *StateSetTool) ClassifyAction(args map[string]interface{}) tools.ActionClassification {
	return tools.ActionClassification{
		Tier:        tools.TierRead,
		Description: "write agent's own state (internal bookkeeping, not an external mutation)",
	}
}

// --- state.delete ---

// StateDeleteTool removes a key from agent state.
type StateDeleteTool struct {
	manager   *Manager
	agentName string
	namespace string
}

// NewStateDeleteTool creates a state.delete tool bound to a specific agent.
func NewStateDeleteTool(manager *Manager, agentName, namespace string) *StateDeleteTool {
	return &StateDeleteTool{manager: manager, agentName: agentName, namespace: namespace}
}

func (t *StateDeleteTool) Name() string        { return "state.delete" }
func (t *StateDeleteTool) Description() string  {
	return "Remove a key from your persistent state. Use this to clean up resolved issues or expired entries."
}

func (t *StateDeleteTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"key": map[string]interface{}{
				"type":        "string",
				"description": "The key to delete",
			},
		},
		"required": []string{"key"},
	}
}

func (t *StateDeleteTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return "", fmt.Errorf("key is required")
	}

	err := t.manager.Delete(ctx, t.agentName, t.namespace, key)
	if err != nil {
		return "", err
	}

	out, _ := json.MarshalIndent(map[string]interface{}{
		"key":     key,
		"deleted": true,
	}, "", "  ")
	return string(out), nil
}

// Capability implements ClassifiableTool.
func (t *StateDeleteTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{
		Domain:         "state",
		SupportedTiers: []tools.ActionTier{tools.TierRead},
	}
}

// ClassifyAction implements ClassifiableTool.
func (t *StateDeleteTool) ClassifyAction(args map[string]interface{}) tools.ActionClassification {
	return tools.ActionClassification{
		Tier:        tools.TierRead,
		Description: "delete agent's own state entry (internal bookkeeping, not an external mutation)",
	}
}
