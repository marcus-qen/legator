/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tools provides the built-in tool implementations for InfraAgent.
// Tools are the bridge between LLM tool_use requests and actual infrastructure operations.
//
// Each tool registers itself with a ToolRegistry and can be called by the runner.
// Tools receive pre-checked arguments (the engine has already approved the action).
package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/marcus-qen/infraagent/internal/provider"
)

// Tool is the interface for executable tools.
type Tool interface {
	// Name returns the tool's identifier (e.g. "kubectl.get", "http.get").
	Name() string

	// Description returns a human-readable description for the LLM.
	Description() string

	// Parameters returns the JSON Schema for the tool's parameters.
	Parameters() map[string]interface{}

	// Execute runs the tool with the given arguments.
	// Returns the result string or an error.
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}

// Registry holds all available tools for an agent run.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tool names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Definitions returns tool definitions suitable for sending to the LLM.
func (r *Registry) Definitions() []provider.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]provider.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, provider.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
		})
	}
	return defs
}

// Execute runs a tool by name with the given arguments.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	tool, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("tool %q not found in registry", name)
	}
	return tool.Execute(ctx, args)
}

// ExtractTarget builds a target string from tool arguments for engine evaluation.
// This is a best-effort extraction — tools define their own target semantics.
func ExtractTarget(toolName string, args map[string]interface{}) string {
	// kubectl tools: "resource [-n namespace] [name]"
	if resource, ok := args["resource"].(string); ok {
		target := resource
		if ns, ok := args["namespace"].(string); ok && ns != "" {
			target += " -n " + ns
		}
		if name, ok := args["name"].(string); ok && name != "" {
			target += " " + name
		}
		return target
	}

	// HTTP tools: URL
	if url, ok := args["url"].(string); ok {
		return url
	}

	// MCP tools: server + tool
	if server, ok := args["server"].(string); ok {
		if tool, ok := args["tool"].(string); ok {
			return server + "/" + tool
		}
		return server
	}

	// Generic fallback
	if target, ok := args["target"].(string); ok {
		return target
	}

	return fmt.Sprintf("%v", args)
}
