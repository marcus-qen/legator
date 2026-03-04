package server

import (
	"encoding/json"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/mcpclient"
)

// handleListMCPServers returns health/status for all configured MCP client servers.
//
// GET /api/v1/mcp/servers
func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.mcpRegistry == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": []any{}})
		return
	}
	servers := s.mcpRegistry.ListServers()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"servers": servers})
}

// handleListMCPTools returns all available tools aggregated from connected MCP servers.
//
// GET /api/v1/mcp/tools
func (s *Server) handleListMCPTools(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.mcpRegistry == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"tools": []any{}})
		return
	}
	tools, err := s.mcpRegistry.ListTools(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "mcp_error", err.Error())
		return
	}

	type toolView struct {
		Server        string `json:"server"`
		QualifiedName string `json:"qualified_name"`
		Name          string `json:"name"`
		Description   string `json:"description,omitempty"`
		InputSchema   any    `json:"input_schema,omitempty"`
	}

	out := make([]toolView, 0, len(tools))
	for _, te := range tools {
		out = append(out, toolView{
			Server:        te.Server,
			QualifiedName: te.QualifiedName,
			Name:          te.Tool.Name,
			Description:   te.Tool.Description,
			InputSchema:   te.Tool.InputSchema,
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"tools": out})
}

// invokeMCPRequest is the request body for POST /api/v1/mcp/invoke.
type invokeMCPRequest struct {
	// Server is the server name. Required if QualifiedName is not set.
	Server string `json:"server,omitempty"`
	// Tool is the tool name. Required if QualifiedName is not set.
	Tool string `json:"tool,omitempty"`
	// QualifiedName is "<server>/<tool>" (alternative to Server+Tool).
	QualifiedName string `json:"qualified_name,omitempty"`
	// Arguments are passed to the tool.
	Arguments map[string]any `json:"arguments,omitempty"`
}

// handleInvokeMCPTool invokes an MCP tool on an external server.
//
// POST /api/v1/mcp/invoke
func (s *Server) handleInvokeMCPTool(w http.ResponseWriter, r *http.Request) {
	if s.mcpRegistry == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "mcp_unavailable", "no MCP client servers configured")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	var req invokeMCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return
	}

	var (
		res *mcpclient.LLMToolResult
		err error
	)

	bridge := mcpclient.NewBridge(s.mcpRegistry)

	if req.QualifiedName != "" {
		res, err = bridge.Invoke(r.Context(), mcpclient.LLMToolCall{
			QualifiedName: req.QualifiedName,
			Arguments:     req.Arguments,
		})
	} else if req.Server != "" && req.Tool != "" {
		res, err = bridge.Invoke(r.Context(), mcpclient.LLMToolCall{
			QualifiedName: req.Server + "/" + req.Tool,
			Arguments:     req.Arguments,
		})
	} else {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "provide qualified_name or both server and tool")
		return
	}

	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "mcp_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"qualified_name": res.QualifiedName,
		"content":        res.Content,
		"is_error":       res.IsError,
	})
}
