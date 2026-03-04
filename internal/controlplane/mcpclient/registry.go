package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// ToolEntry is a discovered tool together with the server it lives on.
type ToolEntry struct {
	// Server is the name of the originating MCP server.
	Server string
	// QualifiedName is "<server>/<tool>".
	QualifiedName string
	// Tool is the raw MCP tool definition.
	Tool *mcp.Tool
}

// ServerStatus reports the connection health of a single server.
type ServerStatus struct {
	Name      string    `json:"name"`
	Connected bool      `json:"connected"`
	Transport string    `json:"transport"`
	ToolCount int       `json:"tool_count"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Registry manages connections to multiple external MCP servers and provides
// an aggregated view of their tools.
type Registry struct {
	mu       sync.RWMutex
	clients  map[string]*ServerClient
	statuses map[string]*ServerStatus
	logger   *zap.Logger
}

// NewRegistry creates an empty registry.
func NewRegistry(logger *zap.Logger) *Registry {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Registry{
		clients:  make(map[string]*ServerClient),
		statuses: make(map[string]*ServerStatus),
		logger:   logger.Named("mcp.registry"),
	}
}

// Add connects to a server using the provided config and registers it.
// If a server with the same name already exists it is replaced.
func (r *Registry) Add(ctx context.Context, cfg ServerConfig) error {
	sc, err := Connect(ctx, cfg, r.logger)
	if err != nil {
		r.mu.Lock()
		r.statuses[cfg.Name] = &ServerStatus{
			Name:      cfg.Name,
			Connected: false,
			Transport: string(cfg.Transport),
			Error:     err.Error(),
		}
		r.mu.Unlock()
		return err
	}

	// Fetch initial tool list for health tracking
	tools, err := sc.ListTools(ctx)
	count := 0
	if err != nil {
		r.logger.Warn("could not list tools after connect", zap.String("server", cfg.Name), zap.Error(err))
	} else {
		count = len(tools)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Close old client if present
	if old, ok := r.clients[cfg.Name]; ok {
		_ = old.Close()
	}

	r.clients[cfg.Name] = sc
	r.statuses[cfg.Name] = &ServerStatus{
		Name:      cfg.Name,
		Connected: true,
		Transport: string(cfg.Transport),
		ToolCount: count,
		LastSeen:  time.Now().UTC(),
	}
	return nil
}

// Remove disconnects and removes a named server.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sc, ok := r.clients[name]; ok {
		_ = sc.Close()
		delete(r.clients, name)
	}
	delete(r.statuses, name)
}

// ListServers returns health status for all registered servers.
func (r *Registry) ListServers() []ServerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServerStatus, 0, len(r.statuses))
	for _, s := range r.statuses {
		out = append(out, *s)
	}
	return out
}

// ListTools aggregates tools from all connected servers.
// Tools are namespaced as "<server>/<tool>".
func (r *Registry) ListTools(ctx context.Context) ([]ToolEntry, error) {
	r.mu.RLock()
	clients := make(map[string]*ServerClient, len(r.clients))
	for k, v := range r.clients {
		clients[k] = v
	}
	r.mu.RUnlock()

	var out []ToolEntry
	var errs []string

	for name, sc := range clients {
		tools, err := sc.ListTools(ctx)
		if err != nil {
			r.logger.Warn("list tools failed", zap.String("server", name), zap.Error(err))
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			r.markUnhealthy(name, err)
			continue
		}
		r.markHealthy(name, len(tools))
		for _, t := range tools {
			out = append(out, ToolEntry{
				Server:        name,
				QualifiedName: name + "/" + t.Name,
				Tool:          t,
			})
		}
	}

	// Return partial results with combined error
	if len(errs) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("mcpclient: all servers failed: %v", errs)
	}
	return out, nil
}

// CallTool invokes a tool identified by qualified name ("<server>/<tool>")
// or by (serverName, toolName) pair.
func (r *Registry) CallTool(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
	r.mu.RLock()
	sc, ok := r.clients[serverName]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("mcpclient: no connected server %q", serverName)
	}
	return sc.CallTool(ctx, toolName, arguments)
}

// CallToolByQualifiedName parses "<server>/<tool>" and calls the tool.
func (r *Registry) CallToolByQualifiedName(ctx context.Context, qualifiedName string, arguments map[string]any) (*mcp.CallToolResult, error) {
	server, tool, err := splitQualifiedName(qualifiedName)
	if err != nil {
		return nil, err
	}
	return r.CallTool(ctx, server, tool, arguments)
}

// Close shuts down all managed clients.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, sc := range r.clients {
		if err := sc.Close(); err != nil {
			r.logger.Warn("close server client", zap.String("server", name), zap.Error(err))
		}
	}
	r.clients = make(map[string]*ServerClient)
	r.statuses = make(map[string]*ServerStatus)
}

// RawToolSchema returns the JSON-encoded inputSchema for a qualified tool name.
func (r *Registry) RawToolSchema(ctx context.Context, qualifiedName string) (json.RawMessage, error) {
	entries, err := r.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.QualifiedName == qualifiedName {
			raw, err := json.Marshal(e.Tool.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("marshal schema: %w", err)
			}
			return raw, nil
		}
	}
	return nil, fmt.Errorf("mcpclient: tool %q not found", qualifiedName)
}

func (r *Registry) markHealthy(name string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.statuses[name]; ok {
		s.Connected = true
		s.ToolCount = count
		s.LastSeen = time.Now().UTC()
		s.Error = ""
	}
}

func (r *Registry) markUnhealthy(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.statuses[name]; ok {
		s.Connected = false
		s.Error = err.Error()
	}
}

// splitQualifiedName splits "server/tool" into its components.
func splitQualifiedName(name string) (server, tool string, err error) {
	for i, ch := range name {
		if ch == '/' {
			return name[:i], name[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("mcpclient: qualified tool name must be \"<server>/<tool>\", got %q", name)
}
