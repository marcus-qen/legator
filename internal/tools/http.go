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

// HTTPCredentialStore maps URL prefixes to authorization headers.
// When a request URL matches a prefix, the corresponding auth header is added.
type HTTPCredentialStore struct {
	entries []credEntry
}

type credEntry struct {
	urlPrefix string
	authValue string // e.g. "Bearer ghp_xxx" or "token xxx"
}

// NewHTTPCredentialStore creates a credential store from endpoint→credential mappings.
func NewHTTPCredentialStore(mappings map[string]string) *HTTPCredentialStore {
	store := &HTTPCredentialStore{}
	for prefix, token := range mappings {
		if token == "" {
			continue
		}
		// Default to Bearer token
		authValue := token
		if !strings.HasPrefix(strings.ToLower(token), "bearer ") &&
			!strings.HasPrefix(strings.ToLower(token), "token ") &&
			!strings.HasPrefix(strings.ToLower(token), "basic ") {
			authValue = "Bearer " + token
		}
		store.entries = append(store.entries, credEntry{urlPrefix: prefix, authValue: authValue})
	}
	return store
}

// AuthHeader returns the authorization header for a URL, if any.
func (s *HTTPCredentialStore) AuthHeader(url string) string {
	if s == nil {
		return ""
	}
	for _, e := range s.entries {
		if strings.HasPrefix(url, e.urlPrefix) {
			return e.authValue
		}
	}
	return ""
}

// HTTPGetTool performs HTTP GET requests.
type HTTPGetTool struct {
	client *http.Client
	creds  *HTTPCredentialStore
}

func NewHTTPGetTool() *HTTPGetTool {
	return &HTTPGetTool{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// WithCredentials returns a copy with a credential store attached.
func (t *HTTPGetTool) WithCredentials(creds *HTTPCredentialStore) *HTTPGetTool {
	return &HTTPGetTool{client: t.client, creds: creds}
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

	// Auto-inject credentials for matching URLs
	if req.Header.Get("Authorization") == "" {
		if auth := t.creds.AuthHeader(url); auth != "" {
			req.Header.Set("Authorization", auth)
		}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	return formatHTTPResponse(resp.StatusCode, resp.Status, body), nil
}

// maxResponseBytes limits HTTP response bodies to prevent token waste.
// Health checks and API endpoints rarely need more than this.
const maxResponseBytes = 8 * 1024 // 8KB

// formatHTTPResponse formats an HTTP response, truncating oversized bodies.
func formatHTTPResponse(statusCode int, status string, body []byte) string {
	bodyStr := string(body)
	if len(body) >= maxResponseBytes {
		bodyStr = bodyStr[:maxResponseBytes-100] + "\n\n... [truncated at 8KB]"
	}
	return fmt.Sprintf("HTTP %d %s\n\n%s", statusCode, status, bodyStr)
}

// HTTPPostTool performs HTTP POST requests.
type HTTPPostTool struct {
	client *http.Client
}

func NewHTTPPostTool() *HTTPPostTool {
	return &HTTPPostTool{
		client: &http.Client{Timeout: 10 * time.Second},
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	return formatHTTPResponse(resp.StatusCode, resp.Status, body), nil
}

// HTTPDeleteTool performs HTTP DELETE requests.
type HTTPDeleteTool struct {
	client *http.Client
}

func NewHTTPDeleteTool() *HTTPDeleteTool {
	return &HTTPDeleteTool{
		client: &http.Client{Timeout: 10 * time.Second},
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	return formatHTTPResponse(resp.StatusCode, resp.Status, body), nil
}
