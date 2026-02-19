/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package provider defines the LLM provider abstraction and implementations.
// Each provider translates between the InfraAgent tool-use protocol and
// a specific LLM API (Anthropic, OpenAI-compatible, etc.).
package provider

import (
	"context"
	"fmt"
)

// Provider is the interface for LLM backends.
// Implementations must be safe for concurrent use.
type Provider interface {
	// Complete sends a completion request and returns the response.
	// The response may contain text content, tool calls, or both.
	Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)

	// Name returns the provider identifier (e.g. "anthropic", "openai").
	Name() string
}

// CompletionRequest is the input to an LLM completion call.
type CompletionRequest struct {
	// SystemPrompt is the system-level instruction (assembled prompt).
	SystemPrompt string

	// Messages is the conversation history.
	Messages []Message

	// Tools is the list of available tools the LLM may call.
	Tools []ToolDefinition

	// Model is the specific model ID (e.g. "claude-sonnet-4-20250514").
	Model string

	// MaxTokens is the maximum output tokens.
	MaxTokens int32
}

// Message represents a single message in the conversation.
type Message struct {
	// Role is "user", "assistant", or "tool".
	Role string

	// Content is the text content (for user/assistant messages).
	Content string

	// ToolCalls is populated when the assistant requests tool execution.
	ToolCalls []ToolCall

	// ToolResults is populated when returning tool execution results.
	ToolResults []ToolResult
}

// ToolCall represents the LLM requesting execution of a tool.
type ToolCall struct {
	// ID is a unique identifier for this tool call (provider-assigned).
	ID string

	// Name is the tool function name.
	Name string

	// Args is the parsed arguments.
	Args map[string]interface{}

	// RawArgs is the raw JSON arguments string (for logging).
	RawArgs string
}

// ToolResult represents the result of executing a tool.
type ToolResult struct {
	// ToolCallID links back to the originating ToolCall.
	ToolCallID string

	// Content is the tool output.
	Content string

	// IsError indicates the tool returned an error.
	IsError bool
}

// ToolDefinition describes a tool the LLM may call.
type ToolDefinition struct {
	// Name is the tool function name.
	Name string

	// Description explains what the tool does.
	Description string

	// Parameters is the JSON Schema for the tool's parameters.
	Parameters map[string]interface{}
}

// CompletionResponse is the output of an LLM completion call.
type CompletionResponse struct {
	// Content is the text response (may be empty if only tool calls).
	Content string

	// ToolCalls is populated when the LLM wants to execute tools.
	ToolCalls []ToolCall

	// Usage reports token consumption.
	Usage UsageInfo

	// StopReason explains why the LLM stopped generating.
	// Common values: "end_turn", "tool_use", "max_tokens".
	StopReason string
}

// HasToolCalls returns true if the response contains tool call requests.
func (r *CompletionResponse) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}

// UsageInfo reports token consumption for a single completion call.
type UsageInfo struct {
	InputTokens  int64
	OutputTokens int64
}

// TotalTokens returns input + output.
func (u UsageInfo) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens
}

// ProviderConfig holds configuration for creating a provider.
type ProviderConfig struct {
	// Type is the provider type: "anthropic", "openai".
	Type string

	// Endpoint is the API base URL (empty for default).
	Endpoint string

	// APIKey is the API key (for apiKey auth).
	APIKey string

	// CustomHeaders are additional headers to send.
	CustomHeaders map[string]string

	// MaxRetries is the number of retries on transient failure (default 3).
	MaxRetries int

	// TimeoutSeconds is the per-request timeout (default 120).
	TimeoutSeconds int
}

// NewProvider creates a provider from config.
func NewProvider(cfg ProviderConfig) (Provider, error) {
	switch cfg.Type {
	case "anthropic":
		return NewAnthropicProvider(cfg)
	case "openai":
		return NewOpenAIProvider(cfg)
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", cfg.Type)
	}
}
