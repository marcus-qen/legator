package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/config"
	"go.uber.org/zap"
)

// newTestServerMCPEnabled creates a test server with MCPEnabled=true so that
// the built-in MCP server is initialised and its tools are registered.
func newTestServerMCPEnabled(t *testing.T) *Server {
	t.Helper()
	t.Setenv("LEGATOR_LLM_PROVIDER", "")
	t.Setenv("LEGATOR_AUTH", "0")
	t.Setenv("LEGATOR_SIGNING_KEY", strings.Repeat("a", 64))

	cfg := config.Config{
		ListenAddr: ":0",
		DataDir:    t.TempDir(),
		MCPEnabled: true,
	}
	logger := zap.NewNop()
	srv, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("new server with MCP enabled: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// TestHandleListMCPTools_BuiltinTools verifies that the /api/v1/mcp/tools endpoint
// returns non-empty results when the MCP server is enabled (GAP-3).
func TestHandleListMCPTools_BuiltinTools(t *testing.T) {
	srv := newTestServerMCPEnabled(t)

	// Sanity check: mcpServer must be set when MCPEnabled=true.
	if srv.mcpServer == nil {
		t.Fatal("expected mcpServer to be non-nil when MCPEnabled=true")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/tools", nil)
	w := httptest.NewRecorder()
	srv.handleListMCPTools(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	var body struct {
		Tools []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(body.Tools) == 0 {
		t.Fatal("expected at least one tool, got empty list")
	}

	// All built-in tools should have source="builtin".
	for _, tool := range body.Tools {
		if tool.Source != "builtin" {
			t.Errorf("tool %q has source=%q, want builtin", tool.Name, tool.Source)
		}
	}

	// Check that at least one well-known tool is present.
	found := false
	for _, tool := range body.Tools {
		if tool.Name == "legator_list_probes" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected legator_list_probes in tools list; got: %+v", body.Tools)
	}
}

// TestHandleOpenAPISpec_ReturnsYAML verifies that GET /api/v1/openapi.yaml returns
// 200 with valid YAML content (GAP-4).
func TestHandleOpenAPISpec_ReturnsYAML(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
	w := httptest.NewRecorder()
	srv.handleOpenAPISpec(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("expected Content-Type application/yaml, got %q", ct)
	}

	// Body should start with a YAML preamble (the OpenAPI spec begins with "openapi:")
	var buf [512]byte
	n, _ := res.Body.Read(buf[:])
	body := string(buf[:n])
	if !strings.Contains(body, "openapi:") {
		t.Errorf("expected YAML body to contain openapi:, got: %q", body)
	}
}
