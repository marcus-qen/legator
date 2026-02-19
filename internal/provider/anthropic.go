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

const (
	anthropicDefaultEndpoint = "https://api.anthropic.com"
	anthropicAPIVersion      = "2023-06-01"
)

// AnthropicProvider calls the Anthropic Messages API.
type AnthropicProvider struct {
	endpoint   string
	apiKey     string
	headers    map[string]string
	client     *http.Client
	maxRetries int
}

// NewAnthropicProvider creates an Anthropic provider.
func NewAnthropicProvider(cfg ProviderConfig) (*AnthropicProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic provider requires API key")
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = anthropicDefaultEndpoint
	}

	timeout := cfg.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}

	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	return &AnthropicProvider{
		endpoint:   endpoint,
		apiKey:     cfg.APIKey,
		headers:    cfg.CustomHeaders,
		client:     &http.Client{Timeout: time.Duration(timeout) * time.Second},
		maxRetries: maxRetries,
	}, nil
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// --- Anthropic API types ---

type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int32               `json:"max_tokens"`
	System    string              `json:"system,omitempty"`
	Messages  []anthropicMessage  `json:"messages"`
	Tools     []anthropicTool     `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
	Error      *anthropicError         `json:"error,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (p *AnthropicProvider) Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	apiReq, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var apiResp anthropicResponse
	if err := p.doWithRetry(ctx, body, &apiResp); err != nil {
		return nil, err
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("anthropic API error (%s): %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	return p.parseResponse(&apiResp), nil
}

func (p *AnthropicProvider) buildRequest(req *CompletionRequest) (*anthropicRequest, error) {
	apiReq := &anthropicRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    req.SystemPrompt,
	}

	if apiReq.MaxTokens <= 0 {
		apiReq.MaxTokens = 4096
	}

	// Convert messages
	for _, msg := range req.Messages {
		am, err := toAnthropicMessage(msg)
		if err != nil {
			return nil, err
		}
		apiReq.Messages = append(apiReq.Messages, am)
	}

	// Convert tools
	for _, tool := range req.Tools {
		apiReq.Tools = append(apiReq.Tools, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}

	return apiReq, nil
}

func toAnthropicMessage(msg Message) (anthropicMessage, error) {
	am := anthropicMessage{Role: msg.Role}

	switch msg.Role {
	case "user":
		if len(msg.ToolResults) > 0 {
			// Tool results are sent as user messages with tool_result content blocks
			var blocks []anthropicContentBlock
			for _, tr := range msg.ToolResults {
				block := anthropicContentBlock{
					Type: "tool_result",
					ID:   tr.ToolCallID,
				}
				// For tool results, content goes in the text field at top level
				if tr.IsError {
					block.Type = "tool_result"
					block.Text = tr.Content
				} else {
					block.Text = tr.Content
				}
				blocks = append(blocks, block)
			}
			content, err := json.Marshal(blocks)
			if err != nil {
				return am, err
			}
			am.Content = content
		} else {
			content, _ := json.Marshal(msg.Content)
			am.Content = content
		}

	case "assistant":
		if len(msg.ToolCalls) > 0 {
			var blocks []anthropicContentBlock
			if msg.Content != "" {
				blocks = append(blocks, anthropicContentBlock{
					Type: "text",
					Text: msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				inputJSON, _ := json.Marshal(tc.Args)
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: inputJSON,
				})
			}
			content, err := json.Marshal(blocks)
			if err != nil {
				return am, err
			}
			am.Content = content
		} else {
			content, _ := json.Marshal(msg.Content)
			am.Content = content
		}

	default:
		content, _ := json.Marshal(msg.Content)
		am.Content = content
	}

	return am, nil
}

func (p *AnthropicProvider) parseResponse(apiResp *anthropicResponse) *CompletionResponse {
	resp := &CompletionResponse{
		StopReason: apiResp.StopReason,
		Usage: UsageInfo{
			InputTokens:  apiResp.Usage.InputTokens,
			OutputTokens: apiResp.Usage.OutputTokens,
		},
	}

	for _, block := range apiResp.Content {
		switch block.Type {
		case "text":
			resp.Content += block.Text
		case "tool_use":
			tc := ToolCall{
				ID:   block.ID,
				Name: block.Name,
			}
			if block.Input != nil {
				tc.RawArgs = string(block.Input)
				_ = json.Unmarshal(block.Input, &tc.Args)
			}
			resp.ToolCalls = append(resp.ToolCalls, tc)
		}
	}

	return resp
}

func (p *AnthropicProvider) doWithRetry(ctx context.Context, body []byte, result *anthropicResponse) error {
	url := p.endpoint + "/v1/messages"

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
		httpReq.Header.Set("x-api-key", p.apiKey)
		httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
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

		// Retry on 429 (rate limit) and 5xx (server errors)
		if httpResp.StatusCode == 429 || httpResp.StatusCode >= 500 {
			if attempt < p.maxRetries {
				continue
			}
			return fmt.Errorf("anthropic API returned %d after %d retries: %s",
				httpResp.StatusCode, p.maxRetries, string(respBody))
		}

		if httpResp.StatusCode != 200 {
			return fmt.Errorf("anthropic API returned %d: %s", httpResp.StatusCode, string(respBody))
		}

		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}

		return nil
	}

	return fmt.Errorf("exhausted retries")
}
