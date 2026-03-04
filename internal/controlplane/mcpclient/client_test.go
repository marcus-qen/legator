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

// startMockSSEServer creates a minimal test HTTP server that exposes an MCP
// SSE endpoint backed by an in-memory MCP server with one tool.
func startMockSSEServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "test-srv", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Echoes input",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: hello"}},
		}, nil
	})

	handler := mcp.NewSSEHandler(func(_ *http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestConnect_SSE(t *testing.T) {
	ts := startMockSSEServer(t)

	cfg := mcpclient.ServerConfig{
		Name:           "test",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
		CallTimeout:    10 * time.Second,
	}

	sc, err := mcpclient.Connect(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sc.Close()

	if sc.Name() != "test" {
		t.Errorf("Name() = %q, want %q", sc.Name(), "test")
	}
}

func TestListTools_SSE(t *testing.T) {
	ts := startMockSSEServer(t)

	cfg := mcpclient.ServerConfig{
		Name:           "test",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
		CallTimeout:    10 * time.Second,
	}
	sc, err := mcpclient.Connect(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sc.Close()

	tools, err := sc.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("tool name = %q, want %q", tools[0].Name, "echo")
	}
}

func TestCallTool_SSE(t *testing.T) {
	ts := startMockSSEServer(t)

	cfg := mcpclient.ServerConfig{
		Name:           "test",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
		CallTimeout:    10 * time.Second,
	}
	sc, err := mcpclient.Connect(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sc.Close()

	res, err := sc.CallTool(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected tool error")
	}
	if len(res.Content) == 0 {
		t.Fatal("no content in result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	if tc.Text != "echo: hello" {
		t.Errorf("text = %q, want %q", tc.Text, "echo: hello")
	}
}

func TestConnect_InvalidTransport(t *testing.T) {
	cfg := mcpclient.ServerConfig{
		Name:      "bad",
		Transport: "grpc",
	}
	_, err := mcpclient.Connect(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("expected error for unknown transport")
	}
}

func TestConnect_StdioMissingCommand(t *testing.T) {
	cfg := mcpclient.ServerConfig{
		Name:      "bad",
		Transport: mcpclient.TransportStdio,
	}
	_, err := mcpclient.Connect(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestCallTool_Timeout(t *testing.T) {
	ts := startMockSSEServer(t)

	cfg := mcpclient.ServerConfig{
		Name:           "test",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
		CallTimeout:    1 * time.Millisecond, // very short to force timeout
	}
	sc, err := mcpclient.Connect(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sc.Close()

	// With a 1ms timeout, this may or may not succeed depending on timing;
	// the important thing is it doesn't panic or deadlock.
	_, _ = sc.CallTool(context.Background(), "echo", map[string]any{"text": "hi"})
}
