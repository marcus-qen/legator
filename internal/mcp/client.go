/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package mcp provides the MCP (Model Context Protocol) client integration
// for InfraAgent. It connects to external MCP tool servers, discovers their
// tools, and bridges them into the InfraAgent tool registry so the engine
// and runner can use them like any built-in tool.
//
// Transport modes supported:
//   - Streamable HTTP (primary) — connects to servers running HTTP endpoints
//   - Stdio (planned) — for sidecar/subprocess MCP servers
//
// Tool names are namespaced: "mcp.<server>.<tool>" to avoid collisions
// with built-in tools (kubectl.*, http.*).
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
	"github.com/marcus-qen/infraagent/internal/tools"
)

// ServerConnection represents a live connection to an MCP server.
type ServerConnection struct {
	// Name is the environment-defined name for this server.
	Name string

	// Endpoint is the URL of the MCP server.
	Endpoint string

	// Capabilities are the declared capabilities (from AgentEnvironment).
	Capabilities []string

	// Session is the active MCP client session.
	Session *mcpsdk.ClientSession

	// Tools are the tools discovered from this server.
	Tools []*mcpsdk.Tool

	// Healthy indicates whether the server passed health check.
	Healthy bool

	// Error holds the last connection error (if any).
	Error error
}

// Manager manages connections to multiple MCP servers.
// It reads server specs from the AgentEnvironment, connects to each,
// discovers tools, and registers them with the tool registry.
type Manager struct {
	log         logr.Logger
	client      *mcpsdk.Client
	connections map[string]*ServerConnection
	mu          sync.RWMutex

	// httpTimeout is the timeout for HTTP transport connections.
	httpTimeout time.Duration

	// NoiseFilters are functions that filter out unwanted tool results.
	NoiseFilters []NoiseFilter
}

// NoiseFilter can modify or suppress MCP tool results.
// Return empty string to suppress the result entirely.
type NoiseFilter func(serverName, toolName, result string) string

// NewManager creates a new MCP Manager.
func NewManager(log logr.Logger) *Manager {
	return &Manager{
		log: log.WithName("mcp"),
		client: mcpsdk.NewClient(
			&mcpsdk.Implementation{
				Name:    "infraagent",
				Version: "0.1.0",
			},
			nil,
		),
		connections: make(map[string]*ServerConnection),
		httpTimeout: 30 * time.Second,
	}
}

// ConnectAll connects to all MCP servers defined in the environment.
// It logs warnings for servers that fail to connect but does not fail —
// agents should degrade gracefully when optional MCP servers are unavailable.
func (m *Manager) ConnectAll(ctx context.Context, servers map[string]corev1alpha1.MCPServerSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, spec := range servers {
		conn := &ServerConnection{
			Name:         name,
			Endpoint:     spec.Endpoint,
			Capabilities: spec.Capabilities,
		}

		if err := m.connectOne(ctx, conn); err != nil {
			conn.Error = err
			conn.Healthy = false
			m.log.Error(err, "Failed to connect to MCP server (degrading gracefully)",
				"server", name,
				"endpoint", spec.Endpoint,
			)
		}

		m.connections[name] = conn
	}

	return nil
}

// connectOne establishes a connection to a single MCP server.
func (m *Manager) connectOne(ctx context.Context, conn *ServerConnection) error {
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint: conn.Endpoint,
		HTTPClient: &http.Client{
			Timeout: m.httpTimeout,
		},
		DisableStandaloneSSE: true, // We don't need server-initiated notifications
	}

	session, err := m.client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", conn.Endpoint, err)
	}
	conn.Session = session

	// Discover tools
	result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		conn.Healthy = true // Connected but tool listing failed — still partially useful
		conn.Error = fmt.Errorf("list tools: %w", err)
		m.log.Error(err, "Connected but failed to list tools", "server", conn.Name)
		return nil
	}

	conn.Tools = result.Tools
	conn.Healthy = true
	conn.Error = nil

	m.log.Info("Connected to MCP server",
		"server", conn.Name,
		"endpoint", conn.Endpoint,
		"tools", len(conn.Tools),
	)

	return nil
}

// RegisterTools registers all discovered MCP tools with the given tool registry.
// Tool names are namespaced as "mcp.<server>.<tool>" (e.g. "mcp.k8sgpt.analyze").
func (m *Manager) RegisterTools(registry *tools.Registry) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	registered := 0
	for _, conn := range m.connections {
		if !conn.Healthy || conn.Session == nil {
			continue
		}

		for _, tool := range conn.Tools {
			mcpTool := NewMCPTool(conn.Name, conn.Session, tool, m.NoiseFilters)
			registry.Register(mcpTool)
			registered++
		}
	}

	return registered
}

// HealthCheck pings all connected servers and updates their health status.
func (m *Manager) HealthCheck(ctx context.Context) map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	results := make(map[string]bool, len(m.connections))
	for name, conn := range m.connections {
		if conn.Session == nil {
			results[name] = false
			continue
		}

		err := conn.Session.Ping(ctx, &mcpsdk.PingParams{})
		healthy := err == nil
		conn.Healthy = healthy
		if err != nil {
			conn.Error = err
		}
		results[name] = healthy
	}

	return results
}

// Close closes all MCP server connections.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, conn := range m.connections {
		if conn.Session != nil {
			if err := conn.Session.Close(); err != nil {
				m.log.Error(err, "Failed to close MCP session", "server", name)
			}
		}
	}
	m.connections = make(map[string]*ServerConnection)
}

// Connections returns a snapshot of all server connections (for status reporting).
func (m *Manager) Connections() map[string]*ServerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ServerConnection, len(m.connections))
	for k, v := range m.connections {
		result[k] = v
	}
	return result
}

// ServerNames returns the names of all registered servers.
func (m *Manager) ServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.connections))
	for name := range m.connections {
		names = append(names, name)
	}
	return names
}

// --- MCP Tool Bridge ---

// MCPTool bridges an MCP server tool into the InfraAgent tool registry.
// It implements the tools.Tool interface.
type MCPTool struct {
	serverName   string
	session      *mcpsdk.ClientSession
	tool         *mcpsdk.Tool
	noiseFilters []NoiseFilter
}

// NewMCPTool creates a tool bridge for a single MCP tool.
func NewMCPTool(serverName string, session *mcpsdk.ClientSession, tool *mcpsdk.Tool, filters []NoiseFilter) *MCPTool {
	return &MCPTool{
		serverName:   serverName,
		session:      session,
		tool:         tool,
		noiseFilters: filters,
	}
}

// Name returns the namespaced tool name: "mcp.<server>.<tool>".
func (t *MCPTool) Name() string {
	return fmt.Sprintf("mcp.%s.%s", t.serverName, t.tool.Name)
}

// Description returns the tool's description from the MCP server.
func (t *MCPTool) Description() string {
	desc := t.tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool %s from server %s", t.tool.Name, t.serverName)
	}
	return desc
}

// Parameters returns the JSON Schema for the tool's parameters.
// Converts from MCP's InputSchema to a map for the LLM provider.
func (t *MCPTool) Parameters() map[string]interface{} {
	if t.tool.InputSchema == nil {
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}

	// InputSchema is typically already a map from the SDK
	if m, ok := t.tool.InputSchema.(map[string]interface{}); ok {
		return m
	}

	// Fallback: wrap whatever we got
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

// Execute calls the MCP tool and returns the text result.
func (t *MCPTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	result, err := t.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.tool.Name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("MCP call %s/%s: %w", t.serverName, t.tool.Name, err)
	}

	// Extract text content from the result
	text := extractTextContent(result)

	// Apply noise filters
	for _, filter := range t.noiseFilters {
		text = filter(t.serverName, t.tool.Name, text)
		if text == "" {
			return "(filtered — no actionable content)", nil
		}
	}

	if result.IsError {
		return text, fmt.Errorf("MCP tool error: %s", text)
	}

	return text, nil
}

// extractTextContent extracts text from MCP Content items.
func extractTextContent(result *mcpsdk.CallToolResult) string {
	if result == nil {
		return ""
	}

	var parts []string
	for _, content := range result.Content {
		if tc, ok := content.(*mcpsdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}

	return strings.Join(parts, "\n")
}
