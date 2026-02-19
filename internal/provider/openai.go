/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const openaiDefaultEndpoint = "https://api.openai.com"

// OpenAIProvider calls OpenAI-compatible chat completion APIs.
// Works with OpenAI, Ollama, vLLM, Azure (with endpoint override), etc.
type OpenAIProvider struct {
	endpoint   string
	apiKey     string
	headers    map[string]string
	client     *http.Client
	maxRetries int
}

// NewOpenAIProvider creates an OpenAI-compatible provider.
func NewOpenAIProvider(cfg ProviderConfig) (*OpenAIProvider, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = openaiDefaultEndpoint
	}

	timeout := cfg.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}

	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	return &OpenAIProvider{
		endpoint:   endpoint,
		apiKey:     cfg.APIKey,
		headers:    cfg.CustomHeaders,
		client:     &http.Client{Timeout: time.Duration(timeout) * time.Second},
		maxRetries: maxRetries,
	}, nil
}

func (p *OpenAIProvider) Name() string { return "openai" }

// --- OpenAI API types ---

type openaiRequest struct {
	Model     string           `json:"model"`
	MaxTokens int32            `json:"max_tokens,omitempty"`
	Messages  []openaiMessage  `json:"messages"`
	Tools     []openaiTool     `json:"tools,omitempty"`
}

type openaiMessage struct {
	Role       string               `json:"role"`
	Content    string               `json:"content,omitempty"`
	ToolCalls  []openaiToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiFunction     `json:"function"`
}

type openaiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string              `json:"type"`
	Function openaiToolFunction  `json:"function"`
}

type openaiToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type openaiResponse struct {
	ID      string         `json:"id"`
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
	Error   *openaiError   `json:"error,omitempty"`
}

type openaiChoice struct {
	Index        int           `json:"index"`
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type openaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (p *OpenAIProvider) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	apiReq := p.buildRequest(req)

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var apiResp openaiResponse
	if err := p.doWithRetry(ctx, body, &apiResp); err != nil {
		return nil, err
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("openai API error (%s): %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return p.parseResponse(&apiResp), nil
}

func (p *OpenAIProvider) buildRequest(req *CompletionRequest) *openaiRequest {
	apiReq := &openaiRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
	}

	if apiReq.MaxTokens <= 0 {
		apiReq.MaxTokens = 4096
	}

	// System prompt as first message
	if req.SystemPrompt != "" {
		apiReq.Messages = append(apiReq.Messages, openaiMessage{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	// Convert messages
	for _, msg := range req.Messages {
		apiReq.Messages = append(apiReq.Messages, toOpenAIMessages(msg)...)
	}

	// Convert tools
	for _, tool := range req.Tools {
		apiReq.Tools = append(apiReq.Tools, openaiTool{
			Type: "function",
			Function: openaiToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}

	return apiReq
}

func toOpenAIMessages(msg Message) []openaiMessage {
	switch msg.Role {
	case "user":
		if len(msg.ToolResults) > 0 {
			// Each tool result is a separate "tool" role message
			var msgs []openaiMessage
			for _, tr := range msg.ToolResults {
				msgs = append(msgs, openaiMessage{
					Role:       "tool",
					Content:    tr.Content,
					ToolCallID: tr.ToolCallID,
				})
			}
			return msgs
		}
		return []openaiMessage{{Role: "user", Content: msg.Content}}

	case "assistant":
		am := openaiMessage{Role: "assistant", Content: msg.Content}
		for _, tc := range msg.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Args)
			am.ToolCalls = append(am.ToolCalls, openaiToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: openaiFunction{
					Name:      tc.Name,
					Arguments: string(argsJSON),
				},
			})
		}
		return []openaiMessage{am}

	default:
		return []openaiMessage{{Role: msg.Role, Content: msg.Content}}
	}
}

func (p *OpenAIProvider) parseResponse(apiResp *openaiResponse) *CompletionResponse {
	resp := &CompletionResponse{
		Usage: UsageInfo{
			InputTokens:  apiResp.Usage.PromptTokens,
			OutputTokens: apiResp.Usage.CompletionTokens,
		},
	}

	if len(apiResp.Choices) > 0 {
		choice := apiResp.Choices[0]
		resp.Content = choice.Message.Content
		resp.StopReason = choice.FinishReason

		for _, tc := range choice.Message.ToolCalls {
			toolCall := ToolCall{
				ID:      tc.ID,
				Name:    tc.Function.Name,
				RawArgs: tc.Function.Arguments,
			}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &toolCall.Args)
			resp.ToolCalls = append(resp.ToolCalls, toolCall)
		}
	}

	return resp
}

func (p *OpenAIProvider) doWithRetry(ctx context.Context, body []byte, result *openaiResponse) error {
	url := p.endpoint + "/v1/chat/completions"

	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create HTTP request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		for k, v := range p.headers {
			httpReq.Header.Set(k, v)
		}

		httpResp, err := p.client.Do(httpReq)
		if err != nil {
			if attempt < p.maxRetries {
				continue
			}
			return fmt.Errorf("HTTP request failed: %w", err)
		}

		respBody, err := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}

		if httpResp.StatusCode == 429 || httpResp.StatusCode >= 500 {
			if attempt < p.maxRetries {
				continue
			}
			return fmt.Errorf("openai API returned %d after %d retries: %s",
				httpResp.StatusCode, p.maxRetries, string(respBody))
		}

		if httpResp.StatusCode != 200 {
			return fmt.Errorf("openai API returned %d: %s", httpResp.StatusCode, string(respBody))
		}

		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}

		return nil
	}

	return fmt.Errorf("exhausted retries")
}
