package mcpserver

import (
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// Version is injected from the control-plane build metadata.
var Version = "dev"

// MCPServer exposes Legator control-plane capabilities as MCP tools/resources.
type MCPServer struct {
	server     *mcp.Server
	handler    http.Handler
	fleetStore *fleet.Store
	auditStore *audit.Store
	dispatcher *corecommanddispatch.Service
	logger     *zap.Logger
}

// New creates and wires the MCP server surface for Legator.
func New(
	fleetStore *fleet.Store,
	auditStore *audit.Store,
	hub *cpws.Hub,
	cmdTracker *cmdtracker.Tracker,
	logger *zap.Logger,
) *MCPServer {
	if logger == nil {
		logger = zap.NewNop()
	}

	implVersion := Version
	if implVersion == "" {
		implVersion = "dev"
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "legator",
		Version: implVersion,
	}, nil)

	m := &MCPServer{
		server:     srv,
		fleetStore: fleetStore,
		auditStore: auditStore,
		dispatcher: corecommanddispatch.NewService(hub, cmdTracker),
		logger:     logger.Named("mcp"),
	}

	m.registerTools()
	m.registerResources()
	m.handler = mcp.NewSSEHandler(func(_ *http.Request) *mcp.Server {
		return m.server
	}, nil)

	return m
}

// Handler returns the HTTP SSE transport handler mounted at /mcp.
func (s *MCPServer) Handler() http.Handler {
	if s == nil {
		return http.NotFoundHandler()
	}
	return s.handler
}
