/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
	"github.com/marcus-qen/infraagent/internal/tools"
)

func TestNewManager(t *testing.T) {
	m := NewManager(logr.Discard())
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if len(m.connections) != 0 {
		t.Errorf("expected 0 connections, got %d", len(m.connections))
	}
	if m.httpTimeout == 0 {
		t.Error("httpTimeout should have a default")
	}
}

func TestManagerServerNames(t *testing.T) {
	m := NewManager(logr.Discard())
	m.connections["k8sgpt"] = &ServerConnection{Name: "k8sgpt", Healthy: true}
	m.connections["prometheus"] = &ServerConnection{Name: "prometheus", Healthy: false}

	names := m.ServerNames()
	if len(names) != 2 {
		t.Errorf("expected 2 names, got %d", len(names))
	}
}

func TestManagerConnections(t *testing.T) {
	m := NewManager(logr.Discard())
	m.connections["test"] = &ServerConnection{
		Name:     "test",
		Endpoint: "http://localhost:8089",
		Healthy:  true,
	}

	conns := m.Connections()
	if len(conns) != 1 {
		t.Errorf("expected 1 connection, got %d", len(conns))
	}
	if conns["test"].Endpoint != "http://localhost:8089" {
		t.Errorf("unexpected endpoint: %s", conns["test"].Endpoint)
	}
}

func TestConnectAllGracefulDegradation(t *testing.T) {
	m := NewManager(logr.Discard())

	// Connecting to a non-existent server should not return an error
	// (graceful degradation)
	servers := map[string]corev1alpha1.MCPServerSpec{
		"nonexistent": {
			Endpoint:     "http://127.0.0.1:1", // Will fail to connect
			Capabilities: []string{"test.analyze"},
		},
	}

	err := m.ConnectAll(context.Background(), servers)
	if err != nil {
		t.Fatalf("ConnectAll should not fail on unreachable servers: %v", err)
	}

	// Connection should exist but be unhealthy
	conns := m.Connections()
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
	if conns["nonexistent"].Healthy {
		t.Error("connection to nonexistent server should not be healthy")
	}
	if conns["nonexistent"].Error == nil {
		t.Error("connection error should be recorded")
	}
}

func TestMCPToolName(t *testing.T) {
	tool := NewMCPTool("k8sgpt", nil, &mcpsdk.Tool{
		Name:        "analyze",
		Description: "Analyze cluster for issues",
	}, nil)

	if name := tool.Name(); name != "mcp.k8sgpt.analyze" {
		t.Errorf("Name() = %q, want %q", name, "mcp.k8sgpt.analyze")
	}
}

func TestMCPToolDescription(t *testing.T) {
	tool := NewMCPTool("k8sgpt", nil, &mcpsdk.Tool{
		Name:        "analyze",
		Description: "Analyze cluster for issues",
	}, nil)

	if desc := tool.Description(); desc != "Analyze cluster for issues" {
		t.Errorf("Description() = %q, want %q", desc, "Analyze cluster for issues")
	}

	// Empty description fallback
	tool2 := NewMCPTool("k8sgpt", nil, &mcpsdk.Tool{Name: "analyze"}, nil)
	if desc := tool2.Description(); desc == "" {
		t.Error("Description() should provide fallback for empty description")
	}
}

func TestMCPToolParametersNil(t *testing.T) {
	tool := NewMCPTool("test", nil, &mcpsdk.Tool{
		Name:        "noop",
		InputSchema: nil,
	}, nil)

	params := tool.Parameters()
	if params == nil {
		t.Fatal("Parameters() should not return nil")
	}
	if params["type"] != "object" {
		t.Errorf("type = %v, want 'object'", params["type"])
	}
}

func TestMCPToolParametersMap(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"filter": map[string]interface{}{
				"type":        "string",
				"description": "analyzer filter",
			},
		},
	}

	tool := NewMCPTool("test", nil, &mcpsdk.Tool{
		Name:        "analyze",
		InputSchema: schema,
	}, nil)

	params := tool.Parameters()
	if params["type"] != "object" {
		t.Errorf("type = %v, want 'object'", params["type"])
	}
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties should be a map")
	}
	if _, ok := props["filter"]; !ok {
		t.Error("missing 'filter' property")
	}
}

func TestExtractTextContent(t *testing.T) {
	result := &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "line 1"},
			&mcpsdk.TextContent{Text: "line 2"},
		},
	}

	text := extractTextContent(result)
	if text != "line 1\nline 2" {
		t.Errorf("extractTextContent = %q, want %q", text, "line 1\nline 2")
	}
}

func TestExtractTextContentNil(t *testing.T) {
	text := extractTextContent(nil)
	if text != "" {
		t.Errorf("extractTextContent(nil) = %q, want empty", text)
	}
}

func TestExtractTextContentEmpty(t *testing.T) {
	result := &mcpsdk.CallToolResult{}
	text := extractTextContent(result)
	if text != "" {
		t.Errorf("extractTextContent(empty) = %q, want empty", text)
	}
}

func TestRegisterToolsSkipsUnhealthy(t *testing.T) {
	m := NewManager(logr.Discard())
	m.connections["healthy"] = &ServerConnection{
		Name:    "healthy",
		Healthy: false, // Unhealthy — should be skipped
		Tools: []*mcpsdk.Tool{
			{Name: "analyze", Description: "test"},
		},
	}

	registry := tools.NewRegistry()
	count := m.RegisterTools(registry)

	if count != 0 {
		t.Errorf("RegisterTools should skip unhealthy servers, got %d", count)
	}
	if len(registry.List()) != 0 {
		t.Errorf("Registry should be empty, got %d tools", len(registry.List()))
	}
}

// TestInMemoryMCPIntegration tests the full MCP flow using in-memory transport.
func TestInMemoryMCPIntegration(t *testing.T) {
	ctx := context.Background()

	// Create an MCP server with a test tool
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-server", Version: "v1.0.0"},
		nil,
	)
	type analyzeArgs struct {
		Filter string `json:"filter"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "analyze",
		Description: "Analyze cluster",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, args analyzeArgs) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: "2 issues found for filter: " + args.Filter},
			},
		}, nil, nil
	})

	// Connect server and client via in-memory transports
	t1, t2 := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "infraagent", Version: "v0.1.0"},
		nil,
	)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	// List tools
	result, err := clientSession.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "analyze" {
		t.Errorf("tool name = %q, want %q", result.Tools[0].Name, "analyze")
	}

	// Create MCPTool bridge and execute (result.Tools[0] is already *Tool)
	mcpTool := NewMCPTool("k8sgpt", clientSession, result.Tools[0], nil)

	if mcpTool.Name() != "mcp.k8sgpt.analyze" {
		t.Errorf("Name() = %q, want %q", mcpTool.Name(), "mcp.k8sgpt.analyze")
	}

	output, err := mcpTool.Execute(ctx, map[string]interface{}{
		"filter": "Pod",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if output != "2 issues found for filter: Pod" {
		t.Errorf("output = %q, want %q", output, "2 issues found for filter: Pod")
	}

	// Register in tool registry
	registry := tools.NewRegistry()
	registry.Register(mcpTool)

	// Execute via registry
	regOutput, err := registry.Execute(ctx, "mcp.k8sgpt.analyze", map[string]interface{}{
		"filter": "Node",
	})
	if err != nil {
		t.Fatalf("registry Execute: %v", err)
	}
	if regOutput != "2 issues found for filter: Node" {
		t.Errorf("registry output = %q, want %q", regOutput, "2 issues found for filter: Node")
	}
}

// TestInMemoryMCPWithNoiseFilter tests that noise filters work in the full flow.
func TestInMemoryMCPWithNoiseFilter(t *testing.T) {
	ctx := context.Background()

	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-k8sgpt", Version: "v1.0.0"},
		nil,
	)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "analyze",
		Description: "Analyze cluster",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, _ any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: "ConfigMap default/kube-root-ca.crt is unused\nPod backstage/app-xyz is CrashLoopBackOff\nKyverno policy violation on backstage/app-xyz"},
			},
		}, nil, nil
	})

	t1, t2 := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "infraagent", Version: "v0.1.0"},
		nil,
	)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	result, err := clientSession.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// Apply k8sgpt noise filter
	mcpTool := NewMCPTool("k8sgpt", clientSession, result.Tools[0], DefaultNoiseFilters())
	output, err := mcpTool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Should have filtered out kube-root-ca.crt and Kyverno lines
	if strings.Contains(output, "kube-root-ca.crt") {
		t.Error("kube-root-ca.crt should be filtered out")
	}
	if strings.Contains(output, "Kyverno") {
		t.Error("Kyverno policy violation should be filtered out")
	}
	// CrashLoopBackOff should remain
	if !strings.Contains(output, "CrashLoopBackOff") {
		t.Error("CrashLoopBackOff should NOT be filtered out")
	}
}

func TestManagerClose(t *testing.T) {
	m := NewManager(logr.Discard())
	m.connections["test"] = &ServerConnection{
		Name:    "test",
		Session: nil, // No session — Close should handle nil gracefully
		Healthy: false,
	}

	// Should not panic
	m.Close()

	if len(m.connections) != 0 {
		t.Errorf("connections should be empty after Close, got %d", len(m.connections))
	}
}
