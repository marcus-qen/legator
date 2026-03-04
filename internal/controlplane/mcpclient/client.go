// Package mcpclient provides an MCP client for connecting to external MCP servers.
// It supports stdio (subprocess) and SSE (HTTP) transports and implements
// tool discovery and invocation over JSON-RPC 2.0.
package mcpclient

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// TransportType identifies the transport mechanism for an external MCP server.
type TransportType string

const (
	// TransportStdio communicates via subprocess stdin/stdout.
	TransportStdio TransportType = "stdio"
	// TransportSSE communicates via HTTP Server-Sent Events.
	TransportSSE TransportType = "sse"
)

// ServerConfig defines how to connect to an external MCP server.
type ServerConfig struct {
	// Name is a unique identifier (used as namespace prefix for tools).
	Name string
	// Transport selects the communication mechanism.
	Transport TransportType
	// Command and Args are used for stdio transport.
	Command string
	Args    []string
	// Endpoint is the SSE URL (e.g. "http://localhost:8080/mcp").
	Endpoint string
	// ConnectTimeout caps the initialization handshake.
	ConnectTimeout time.Duration
	// CallTimeout caps individual tool calls.
	CallTimeout time.Duration
	// Env holds extra environment variables for stdio transport.
	Env []string
}

// ServerClient wraps a connected MCP client session for one external server.
type ServerClient struct {
	cfg    ServerConfig
	client *mcp.Client
	sess   *mcp.ClientSession
	cancel context.CancelFunc
	logger *zap.Logger
}

// Connect opens the transport, initializes the MCP session and returns a
// ready-to-use ServerClient.  The caller must call Close when done.
func Connect(ctx context.Context, cfg ServerConfig, logger *zap.Logger) (*ServerClient, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	connCtx, cancel := context.WithTimeout(ctx, timeout)

	var transport mcp.Transport
	switch cfg.Transport {
	case TransportStdio:
		if cfg.Command == "" {
			cancel()
			return nil, fmt.Errorf("mcpclient: stdio transport requires a command")
		}
		cmd := exec.CommandContext(connCtx, cfg.Command, cfg.Args...)
		cmd.Env = append(cmd.Environ(), cfg.Env...)
		transport = &mcp.CommandTransport{Command: cmd}
	case TransportSSE:
		if cfg.Endpoint == "" {
			cancel()
			return nil, fmt.Errorf("mcpclient: SSE transport requires an endpoint")
		}
		transport = &mcp.SSEClientTransport{Endpoint: cfg.Endpoint}
	default:
		cancel()
		return nil, fmt.Errorf("mcpclient: unknown transport %q (use \"stdio\" or \"sse\")", cfg.Transport)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{
		Name:    "legator-mcp-client",
		Version: "1.0.0",
	}, nil)

	sess, err := mcpClient.Connect(connCtx, transport, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcpclient: connect to %q: %w", cfg.Name, err)
	}

	logger.Info("mcp client connected", zap.String("server", cfg.Name), zap.String("transport", string(cfg.Transport)))

	return &ServerClient{
		cfg:    cfg,
		client: mcpClient,
		sess:   sess,
		cancel: cancel,
		logger: logger.Named("mcpclient." + cfg.Name),
	}, nil
}

// Name returns the server name.
func (sc *ServerClient) Name() string { return sc.cfg.Name }

// ListTools fetches the tool catalogue from the remote server.
func (sc *ServerClient) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	ctx, cancel := sc.callCtx(ctx)
	defer cancel()

	res, err := sc.sess.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: list tools from %q: %w", sc.cfg.Name, err)
	}
	return res.Tools, nil
}

// CallTool invokes a named tool on the remote server with the given arguments.
func (sc *ServerClient) CallTool(ctx context.Context, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
	ctx, cancel := sc.callCtx(ctx)
	defer cancel()

	res, err := sc.sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("mcpclient: call tool %q on %q: %w", toolName, sc.cfg.Name, err)
	}
	return res, nil
}

// Close tears down the session and underlying transport.
func (sc *ServerClient) Close() error {
	if sc.cancel != nil {
		sc.cancel()
	}
	if sc.sess != nil {
		return sc.sess.Close()
	}
	return nil
}

// callCtx returns a context capped by CallTimeout (if set).
func (sc *ServerClient) callCtx(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := sc.cfg.CallTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}
