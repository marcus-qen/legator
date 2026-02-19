/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPGetTool performs HTTP GET requests.
type HTTPGetTool struct {
	client *http.Client
}

func NewHTTPGetTool() *HTTPGetTool {
	return &HTTPGetTool{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *HTTPGetTool) Name() string { return "http.get" }

func (t *HTTPGetTool) Description() string {
	return "Perform an HTTP GET request. Returns status code and response body."
}

func (t *HTTPGetTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to request",
			},
			"headers": map[string]interface{}{
				"type":        "object",
				"description": "Additional headers",
			},
		},
		"required": []string{"url"},
	}
}

func (t *HTTPGetTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return "", fmt.Errorf("url is required")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	if headers, ok := args["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // 64KB max
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	return fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, resp.Status, string(body)), nil
}

// HTTPPostTool performs HTTP POST requests.
type HTTPPostTool struct {
	client *http.Client
}

func NewHTTPPostTool() *HTTPPostTool {
	return &HTTPPostTool{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *HTTPPostTool) Name() string { return "http.post" }

func (t *HTTPPostTool) Description() string {
	return "Perform an HTTP POST request. Returns status code and response body."
}

func (t *HTTPPostTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to request",
			},
			"body": map[string]interface{}{
				"type":        "string",
				"description": "Request body",
			},
			"contentType": map[string]interface{}{
				"type":        "string",
				"description": "Content-Type header (default: application/json)",
			},
			"headers": map[string]interface{}{
				"type":        "object",
				"description": "Additional headers",
			},
		},
		"required": []string{"url"},
	}
}

func (t *HTTPPostTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return "", fmt.Errorf("url is required")
	}

	bodyStr, _ := args["body"].(string)
	contentType, _ := args["contentType"].(string)
	if contentType == "" {
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(bodyStr))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	if headers, ok := args["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	return fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, resp.Status, string(body)), nil
}

// HTTPDeleteTool performs HTTP DELETE requests.
type HTTPDeleteTool struct {
	client *http.Client
}

func NewHTTPDeleteTool() *HTTPDeleteTool {
	return &HTTPDeleteTool{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *HTTPDeleteTool) Name() string { return "http.delete" }

func (t *HTTPDeleteTool) Description() string {
	return "Perform an HTTP DELETE request. Subject to guardrail enforcement."
}

func (t *HTTPDeleteTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL to request",
			},
			"headers": map[string]interface{}{
				"type":        "object",
				"description": "Additional headers",
			},
		},
		"required": []string{"url"},
	}
}

func (t *HTTPDeleteTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	url, _ := args["url"].(string)
	if url == "" {
		return "", fmt.Errorf("url is required")
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	if headers, ok := args["headers"].(map[string]interface{}); ok {
		for k, v := range headers {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP DELETE %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	return fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, resp.Status, string(body)), nil
}
