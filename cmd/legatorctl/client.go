package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type APIClient struct {
	server string
	apiKey string
	http   *http.Client
}

type FleetSummary struct {
	Counts           map[string]int `json:"counts"`
	Connected        int            `json:"connected"`
	PendingApprovals int            `json:"pending_approvals"`
}

type Probe struct {
	ID          string          `json:"id"`
	Hostname    string          `json:"hostname"`
	OS          string          `json:"os"`
	Arch        string          `json:"arch"`
	Status      string          `json:"status"`
	PolicyLevel string          `json:"policy_level"`
	Registered  time.Time       `json:"registered"`
	LastSeen    time.Time       `json:"last_seen"`
	Tags        []string        `json:"tags,omitempty"`
	Inventory   *ProbeInventory `json:"inventory,omitempty"`
	Health      *ProbeHealth    `json:"health,omitempty"`
}

type ProbeInventory struct {
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Kernel    string `json:"kernel"`
	CPUs      int    `json:"cpus"`
	MemTotal  uint64 `json:"mem_total_bytes"`
	DiskTotal uint64 `json:"disk_total_bytes"`
}

type ProbeHealth struct {
	Score    int      `json:"score"`
	Status   string   `json:"status"`
	Warnings []string `json:"warnings,omitempty"`
}

type APIError struct {
	Error string `json:"error"`
}

type RegistrationToken struct {
	Value   string    `json:"token"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Used    bool      `json:"used"`
}

type APIKey struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	KeyPrefix   string     `json:"key_prefix"`
	Permissions []string   `json:"permissions"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Enabled     bool       `json:"enabled"`
}

type KeyCreatePayload struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

type KeyCreateResponse struct {
	Key      APIKey `json:"key"`
	PlainKey string `json:"plain_key"`
	Warning  string `json:"warning"`
}

type KeyListResponse struct {
	Keys  []APIKey `json:"keys"`
	Total int      `json:"total"`
}

func NewAPIClient(server, apiKey string) *APIClient {
	server = strings.TrimRight(server, "/")
	if server == "" {
		server = "http://localhost:8080"
	}

	return &APIClient{
		server: server,
		apiKey: apiKey,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *APIClient) FleetSummary(ctx context.Context) (*FleetSummary, error) {
	var out FleetSummary
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/fleet/summary", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) Probes(ctx context.Context) ([]Probe, error) {
	var out []Probe
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/probes", nil, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *APIClient) Probe(ctx context.Context, id string) (*Probe, error) {
	var out Probe
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/probes/"+id, nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) SendCommand(ctx context.Context, id, command string, args []string) (map[string]any, error) {
	payload := map[string]any{
		"command": command,
		"args":    args,
	}
	var out map[string]any
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/probes/"+id+"/command", payload, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *APIClient) CreateToken(ctx context.Context) (*RegistrationToken, error) {
	var out RegistrationToken
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/tokens", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) ListKeys(ctx context.Context) (*KeyListResponse, error) {
	var out KeyListResponse
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/auth/keys", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) CreateKey(ctx context.Context, req KeyCreatePayload) (*KeyCreateResponse, error) {
	var out KeyCreateResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/auth/keys", req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) doJSON(ctx context.Context, method, path string, body any, out any) error {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewBuffer(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.server+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	resBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr APIError
		err := json.Unmarshal(resBody, &apiErr)
		if err == nil && apiErr.Error != "" {
			return fmt.Errorf("request failed (status %d): %s", resp.StatusCode, apiErr.Error)
		}
		return fmt.Errorf("request failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(resBody)))
	}

	if out == nil || len(resBody) == 0 {
		return nil
	}

	if err := json.Unmarshal(resBody, out); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}
