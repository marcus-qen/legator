/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package provider

import (
	"context"
	"fmt"
	"sync"
)

// MockProvider is a test double for LLM providers.
// It returns pre-configured responses in order, tracking all requests.
type MockProvider struct {
	mu        sync.Mutex
	responses []*CompletionResponse
	errors    []error
	calls     []*CompletionRequest
	callIndex int
}

// NewMockProvider creates a mock with queued responses.
// Each Complete() call pops the next response/error pair.
func NewMockProvider(responses []*CompletionResponse, errors []error) *MockProvider {
	return &MockProvider{
		responses: responses,
		errors:    errors,
	}
}

// NewMockProviderSimple creates a mock that returns a single text response.
func NewMockProviderSimple(content string) *MockProvider {
	return NewMockProvider(
		[]*CompletionResponse{{
			Content:    content,
			StopReason: "end_turn",
			Usage:      UsageInfo{InputTokens: 100, OutputTokens: 50},
		}},
		[]error{nil},
	)
}

// NewMockProviderWithToolCalls creates a mock that first requests tool calls,
// then returns a final text response.
func NewMockProviderWithToolCalls(toolCalls []ToolCall, finalContent string) *MockProvider {
	return NewMockProvider(
		[]*CompletionResponse{
			{
				ToolCalls:  toolCalls,
				StopReason: "tool_use",
				Usage:      UsageInfo{InputTokens: 100, OutputTokens: 50},
			},
			{
				Content:    finalContent,
				StopReason: "end_turn",
				Usage:      UsageInfo{InputTokens: 200, OutputTokens: 100},
			},
		},
		[]error{nil, nil},
	)
}

func (m *MockProvider) Complete(_ context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, req)

	if m.callIndex >= len(m.responses) {
		return nil, fmt.Errorf("mock provider: no more responses (call #%d)", m.callIndex)
	}

	resp := m.responses[m.callIndex]
	var err error
	if m.callIndex < len(m.errors) {
		err = m.errors[m.callIndex]
	}
	m.callIndex++

	return resp, err
}

func (m *MockProvider) Name() string {
	return "mock"
}

// Calls returns all requests made to this mock.
func (m *MockProvider) Calls() []*CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// CallCount returns how many times Complete was called.
func (m *MockProvider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Reset clears call history and resets the response index.
func (m *MockProvider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.callIndex = 0
}
