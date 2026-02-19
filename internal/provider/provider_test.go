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
	"testing"
)

func TestMockProviderSimple(t *testing.T) {
	mock := NewMockProviderSimple("Hello, world!")

	resp, err := mock.Complete(context.Background(), &CompletionRequest{
		SystemPrompt: "You are a test.",
		Messages:     []Message{{Role: "user", Content: "Hi"}},
		Model:        "test-model",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Content != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("expected 'end_turn', got %q", resp.StopReason)
	}
	if mock.CallCount() != 1 {
		t.Errorf("expected 1 call, got %d", mock.CallCount())
	}
}

func TestMockProviderWithToolCalls(t *testing.T) {
	mock := NewMockProviderWithToolCalls(
		[]ToolCall{{ID: "tc1", Name: "kubectl.get", Args: map[string]interface{}{"resource": "pods"}}},
		"Found 5 pods running.",
	)

	// First call returns tool calls
	resp1, err := mock.Complete(context.Background(), &CompletionRequest{
		Model: "test",
		Messages: []Message{{Role: "user", Content: "Check pods"}},
	})
	if err != nil {
		t.Fatalf("call 1 error: %v", err)
	}
	if !resp1.HasToolCalls() {
		t.Error("expected tool calls in first response")
	}
	if len(resp1.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(resp1.ToolCalls))
	}

	// Second call returns final text
	resp2, err := mock.Complete(context.Background(), &CompletionRequest{
		Model: "test",
		Messages: []Message{
			{Role: "user", Content: "Check pods"},
			{Role: "assistant", ToolCalls: resp1.ToolCalls},
			{Role: "user", ToolResults: []ToolResult{{ToolCallID: "tc1", Content: "5 pods"}}},
		},
	})
	if err != nil {
		t.Fatalf("call 2 error: %v", err)
	}
	if resp2.HasToolCalls() {
		t.Error("expected no tool calls in second response")
	}
	if resp2.Content != "Found 5 pods running." {
		t.Errorf("expected final content, got %q", resp2.Content)
	}

	if mock.CallCount() != 2 {
		t.Errorf("expected 2 calls, got %d", mock.CallCount())
	}
}

func TestMockProviderExhausted(t *testing.T) {
	mock := NewMockProviderSimple("one")

	// First call succeeds
	_, err := mock.Complete(context.Background(), &CompletionRequest{Model: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second call fails (no more responses)
	_, err = mock.Complete(context.Background(), &CompletionRequest{Model: "test"})
	if err == nil {
		t.Error("expected error when mock exhausted")
	}
}

func TestMockProviderReset(t *testing.T) {
	mock := NewMockProviderSimple("test")

	_, _ = mock.Complete(context.Background(), &CompletionRequest{Model: "test"})
	if mock.CallCount() != 1 {
		t.Error("expected 1 call")
	}

	mock.Reset()
	if mock.CallCount() != 0 {
		t.Error("expected 0 calls after reset")
	}

	// Should work again after reset
	resp, err := mock.Complete(context.Background(), &CompletionRequest{Model: "test"})
	if err != nil {
		t.Fatalf("unexpected error after reset: %v", err)
	}
	if resp.Content != "test" {
		t.Error("wrong content after reset")
	}
}

func TestUsageInfo_TotalTokens(t *testing.T) {
	u := UsageInfo{InputTokens: 100, OutputTokens: 50}
	if u.TotalTokens() != 150 {
		t.Errorf("expected 150, got %d", u.TotalTokens())
	}
}

func TestCompletionResponse_HasToolCalls(t *testing.T) {
	resp := &CompletionResponse{}
	if resp.HasToolCalls() {
		t.Error("should not have tool calls")
	}

	resp.ToolCalls = []ToolCall{{ID: "1", Name: "test"}}
	if !resp.HasToolCalls() {
		t.Error("should have tool calls")
	}
}

func TestNewProvider_Unsupported(t *testing.T) {
	_, err := NewProvider(ProviderConfig{Type: "gemini"})
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func TestNewAnthropicProvider_NoKey(t *testing.T) {
	_, err := NewAnthropicProvider(ProviderConfig{})
	if err == nil {
		t.Error("expected error when no API key")
	}
}

func TestMockProviderName(t *testing.T) {
	mock := NewMockProviderSimple("test")
	if mock.Name() != "mock" {
		t.Errorf("expected 'mock', got %q", mock.Name())
	}
}
