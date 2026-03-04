package mcpclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newBridgeWithServer(t *testing.T) (*mcpclient.Bridge, *mcpclient.Registry) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "bridge-srv", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "add",
		Description: "Adds two numbers",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "number"},
				"b": map[string]any{"type": "number"},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "42"}},
		}, nil
	})

	h := mcp.NewSSEHandler(func(_ *http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	reg := mcpclient.NewRegistry(nil)
	t.Cleanup(reg.Close)

	if err := reg.Add(context.Background(), mcpclient.ServerConfig{
		Name:           "math",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	return mcpclient.NewBridge(reg), reg
}

func TestBridge_LLMTools(t *testing.T) {
	bridge, _ := newBridgeWithServer(t)

	tools, err := bridge.LLMTools(context.Background())
	if err != nil {
		t.Fatalf("LLMTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	tool := tools[0]
	if tool.Type != "function" {
		t.Errorf("type = %q, want %q", tool.Type, "function")
	}
	if tool.Function.Name != "math_add" {
		t.Errorf("name = %q, want %q", tool.Function.Name, "math_add")
	}
	if tool.Function.Description == "" {
		t.Error("description should be non-empty")
	}
	if len(tool.Function.Parameters) == 0 {
		t.Error("parameters should be non-empty")
	}
}

func TestBridge_Invoke_LLMName(t *testing.T) {
	bridge, _ := newBridgeWithServer(t)

	res, err := bridge.Invoke(context.Background(), mcpclient.LLMToolCall{
		QualifiedName: "math_add", // LLM-style underscore
		Arguments:     map[string]any{"a": 1, "b": 2},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected tool error: %s", res.Content)
	}
	if res.Content != "42" {
		t.Errorf("content = %q, want %q", res.Content, "42")
	}
}

func TestBridge_Invoke_MCPName(t *testing.T) {
	bridge, _ := newBridgeWithServer(t)

	res, err := bridge.Invoke(context.Background(), mcpclient.LLMToolCall{
		QualifiedName: "math/add", // MCP-style slash
		Arguments:     nil,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected tool error: %s", res.Content)
	}
}

func TestBridge_Invoke_UnknownTool(t *testing.T) {
	bridge, _ := newBridgeWithServer(t)

	// Invoking a tool on a non-existent server should produce an error result (not panic)
	res, err := bridge.Invoke(context.Background(), mcpclient.LLMToolCall{
		QualifiedName: "ghost_tool",
		Arguments:     nil,
	})
	if err != nil {
		// Also acceptable: returns error directly
		return
	}
	if !res.IsError {
		t.Error("expected IsError=true for unknown server")
	}
}
