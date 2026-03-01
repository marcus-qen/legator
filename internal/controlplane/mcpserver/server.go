package mcpserver

import (
	"context"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/grafana"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/controlplane/kubeflow"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

// Version is injected from the control-plane build metadata.
var Version = "dev"

// MCPServer exposes Legator control-plane capabilities as MCP tools/resources.
type MCPServer struct {
	server            *mcp.Server
	handler           http.Handler
	fleetStore        *fleet.Store
	federationStore   *fleet.FederationStore
	auditStore        *audit.Store
	jobsStore         *jobs.Store
	eventBus          *events.Bus
	hub               *cpws.Hub
	dispatcher        *corecommanddispatch.Service
	decideApproval    func(id string, request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error)
	kubeflowRunStatus func(context.Context, kubeflow.RunStatusRequest) (kubeflow.RunStatusResult, error)
	kubeflowSubmitRun func(context.Context, kubeflow.SubmitRunRequest) (map[string]any, error)
	kubeflowCancelRun func(context.Context, kubeflow.CancelRunRequest) (map[string]any, error)
	grafanaClient     grafana.Client
	permissionChecker func(context.Context, auth.Permission) error
	logger            *zap.Logger
}

// Option customizes MCP server wiring.
type Option func(*MCPServer)

// WithKubeflowTools wires Kubeflow run tools when callbacks are available.
func WithKubeflowTools(
	runStatus func(context.Context, kubeflow.RunStatusRequest) (kubeflow.RunStatusResult, error),
	submitRun func(context.Context, kubeflow.SubmitRunRequest) (map[string]any, error),
	cancelRun func(context.Context, kubeflow.CancelRunRequest) (map[string]any, error),
) Option {
	return func(server *MCPServer) {
		if server == nil {
			return
		}
		server.kubeflowRunStatus = runStatus
		server.kubeflowSubmitRun = submitRun
		server.kubeflowCancelRun = cancelRun
	}
}

// WithGrafanaClient wires read-only Grafana tools/resources when the adapter is available.
func WithGrafanaClient(client grafana.Client) Option {
	return func(server *MCPServer) {
		if server == nil {
			return
		}
		server.grafanaClient = client
	}
}

// WithFederationStore wires a preconfigured federation read model for MCP federation tools/resources.
func WithFederationStore(store *fleet.FederationStore) Option {
	return func(server *MCPServer) {
		if server == nil || store == nil {
			return
		}
		server.federationStore = store
	}
}

// WithPermissionChecker enforces permission checks for MCP handlers that opt in.
func WithPermissionChecker(checker func(context.Context, auth.Permission) error) Option {
	return func(server *MCPServer) {
		if server == nil {
			return
		}
		server.permissionChecker = checker
	}
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
	opts ...Option,
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

	federationStore := fleet.NewFederationStore()
	if fleetStore != nil {
		federationStore.RegisterSource(fleet.NewFleetSourceAdapter(fleetStore, fleet.FederationSourceDescriptor{
			ID:      "local",
			Name:    "Local Fleet",
			Kind:    "control-plane",
			Cluster: "primary",
			Site:    "local",
		}))
	}

	m := &MCPServer{
		server:          srv,
		fleetStore:      fleetStore,
		federationStore: federationStore,
		auditStore:      auditStore,
		jobsStore:       jobsStore,
		eventBus:        eventBus,
		hub:             hub,
		dispatcher:      corecommanddispatch.NewService(hub, cmdTracker),
		decideApproval:  decideApproval,
		logger:          logger.Named("mcp"),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
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

func (s *MCPServer) requirePermission(ctx context.Context, perm auth.Permission) error {
	if s == nil || s.permissionChecker == nil {
		return nil
	}
	return s.permissionChecker(ctx, perm)
}
