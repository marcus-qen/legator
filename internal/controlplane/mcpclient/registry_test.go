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

func newTestSSEServer(t *testing.T, tools ...*mcp.Tool) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	for _, tool := range tools {
		toolCopy := tool
		srv.AddTool(toolCopy, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	}
	h := mcp.NewSSEHandler(func(_ *http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestRegistry_AddAndListServers(t *testing.T) {
	ts := newTestSSEServer(t,
		&mcp.Tool{Name: "tool_a", InputSchema: map[string]any{"type": "object"}},
		&mcp.Tool{Name: "tool_b", InputSchema: map[string]any{"type": "object"}},
	)

	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	err := reg.Add(context.Background(), mcpclient.ServerConfig{
		Name:           "srv1",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	servers := reg.ListServers()
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Name != "srv1" {
		t.Errorf("server name = %q, want %q", servers[0].Name, "srv1")
	}
	if !servers[0].Connected {
		t.Errorf("server should be connected")
	}
}

func TestRegistry_ListTools_Namespace(t *testing.T) {
	ts := newTestSSEServer(t,
		&mcp.Tool{Name: "tool_a", InputSchema: map[string]any{"type": "object"}},
	)

	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	if err := reg.Add(context.Background(), mcpclient.ServerConfig{
		Name:           "myserver",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	tools, err := reg.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].QualifiedName != "myserver/tool_a" {
		t.Errorf("qualified name = %q, want %q", tools[0].QualifiedName, "myserver/tool_a")
	}
	if tools[0].Server != "myserver" {
		t.Errorf("server = %q, want %q", tools[0].Server, "myserver")
	}
}

func TestRegistry_CallTool(t *testing.T) {
	ts := newTestSSEServer(t,
		&mcp.Tool{Name: "greet", InputSchema: map[string]any{"type": "object"}},
	)

	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	if err := reg.Add(context.Background(), mcpclient.ServerConfig{
		Name:           "greeter",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	res, err := reg.CallTool(context.Background(), "greeter", "greet", map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
}

func TestRegistry_CallToolByQualifiedName(t *testing.T) {
	ts := newTestSSEServer(t,
		&mcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}},
	)

	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	if err := reg.Add(context.Background(), mcpclient.ServerConfig{
		Name:           "mysrv",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	res, err := reg.CallToolByQualifiedName(context.Background(), "mysrv/ping", nil)
	if err != nil {
		t.Fatalf("CallToolByQualifiedName: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
}

func TestRegistry_CallTool_UnknownServer(t *testing.T) {
	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	_, err := reg.CallTool(context.Background(), "ghost", "tool", nil)
	if err == nil {
		t.Fatal("expected error for unknown server")
	}
}

func TestRegistry_MultiServer_Aggregate(t *testing.T) {
	ts1 := newTestSSEServer(t, &mcp.Tool{Name: "a1", InputSchema: map[string]any{"type": "object"}})
	ts2 := newTestSSEServer(t, &mcp.Tool{Name: "b1", InputSchema: map[string]any{"type": "object"}})

	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	for _, pair := range []struct{ name, url string }{{"s1", ts1.URL}, {"s2", ts2.URL}} {
		if err := reg.Add(context.Background(), mcpclient.ServerConfig{
			Name:           pair.name,
			Transport:      mcpclient.TransportSSE,
			Endpoint:       pair.url,
			ConnectTimeout: 10 * time.Second,
		}); err != nil {
			t.Fatalf("Add %s: %v", pair.name, err)
		}
	}

	tools, err := reg.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestRegistry_Remove(t *testing.T) {
	ts := newTestSSEServer(t, &mcp.Tool{Name: "x", InputSchema: map[string]any{"type": "object"}})

	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	if err := reg.Add(context.Background(), mcpclient.ServerConfig{
		Name:           "temp",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       ts.URL,
		ConnectTimeout: 10 * time.Second,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	reg.Remove("temp")

	servers := reg.ListServers()
	if len(servers) != 0 {
		t.Errorf("expected 0 servers after remove, got %d", len(servers))
	}
}

func TestRegistry_Add_ConnectionError(t *testing.T) {
	reg := mcpclient.NewRegistry(nil)
	defer reg.Close()

	err := reg.Add(context.Background(), mcpclient.ServerConfig{
		Name:           "dead",
		Transport:      mcpclient.TransportSSE,
		Endpoint:       "http://127.0.0.1:1", // nothing listening
		ConnectTimeout: 1 * time.Second,
	})
	if err == nil {
		t.Fatal("expected connection error")
	}

	// Status should record the error
	statuses := reg.ListServers()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status entry, got %d", len(statuses))
	}
	if statuses[0].Connected {
		t.Error("should be marked disconnected")
	}
	if statuses[0].Error == "" {
		t.Error("error field should be set")
	}
}
