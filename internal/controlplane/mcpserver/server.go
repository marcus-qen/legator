package mcpserver

import (
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// Version is injected from the control-plane build metadata.
var Version = "dev"

// MCPServer exposes Legator control-plane capabilities as MCP tools/resources.
type MCPServer struct {
	server         *mcp.Server
	handler        http.Handler
	fleetStore     *fleet.Store
	auditStore     *audit.Store
	jobsStore      *jobs.Store
	eventBus       *events.Bus
	hub            *cpws.Hub
	dispatcher     *corecommanddispatch.Service
	decideApproval func(id string, request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error)
	logger         *zap.Logger
}

// New creates and wires the MCP server surface for Legator.
func New(
	fleetStore *fleet.Store,
	auditStore *audit.Store,
	jobsStore *jobs.Store,
	eventBus *events.Bus,
	hub *cpws.Hub,
	cmdTracker *cmdtracker.Tracker,
	logger *zap.Logger,
	decideApproval func(id string, request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error),
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
		server:         srv,
		fleetStore:     fleetStore,
		auditStore:     auditStore,
		jobsStore:      jobsStore,
		eventBus:       eventBus,
		hub:            hub,
		dispatcher:     corecommanddispatch.NewService(hub, cmdTracker),
		decideApproval: decideApproval,
		logger:         logger.Named("mcp"),
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
