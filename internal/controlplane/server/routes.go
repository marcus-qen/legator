package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/api"
	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/controlplane/metrics"
	"github.com/marcus-qen/legator/internal/controlplane/modeldock"
	controlpolicy "github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/controlplane/tenant"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Health + version
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	// OpenAPI spec (public, no auth required)
	mux.HandleFunc("GET /api/v1/openapi.yaml", s.handleOpenAPISpec)

	// Login/session
	loginOpts := auth.LoginPageOptions{}
	if s.oidcProvider != nil && s.oidcProvider.Enabled() {
		loginOpts.OIDCEnabled = true
		loginOpts.OIDCProviderName = s.oidcProvider.ProviderName()
		mux.HandleFunc("GET /auth/oidc/login", s.oidcProvider.HandleLogin)
		mux.HandleFunc("GET /auth/oidc/callback", s.oidcProvider.HandleCallback(s.userStore, s.sessionCreator))
	}
	mux.HandleFunc("GET /login", auth.HandleLoginPage(filepath.Join("web", "templates"), loginOpts))
	mux.HandleFunc("POST /login", auth.HandleLoginWithAudit(s.userAuth, s.sessionCreator, s.auditRecorder(), loginOpts))
	mux.HandleFunc("POST /logout", auth.HandleLogout(s.sessionDeleter))

	// Current user + RBAC user management (Track 2 stubs)
	mux.HandleFunc("GET /api/v1/me", auth.HandleMe())
	mux.HandleFunc("GET /api/v1/users", s.withPermission(auth.PermAdmin, s.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", s.withPermission(auth.PermAdmin, s.handleCreateUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", s.withPermission(auth.PermAdmin, s.handleDeleteUser))

	// Fleet API
	mux.HandleFunc("POST /api/v1/probes", s.withPermission(auth.PermFleetWrite, s.withTenantScope(s.handleCreateProbe)))
	mux.HandleFunc("GET /api/v1/probes", s.withPermission(auth.PermFleetRead, s.withTenantScope(s.handleListProbes)))
	mux.HandleFunc("GET /api/v1/probes/{id}", s.withPermission(auth.PermFleetRead, s.withTenantScope(s.handleGetProbe)))
	mux.HandleFunc("GET /api/v1/probes/{id}/health", s.withPermission(auth.PermFleetRead, s.handleProbeHealth))
	mux.HandleFunc("POST /api/v1/probes/{id}/command", s.withPermission(auth.PermFleetWrite, s.handleDispatchCommand))
	mux.HandleFunc("POST /api/v1/probes/{id}/command/simulate", s.withPermission(auth.PermFleetWrite, s.handleSimulateCommandPolicy))
	mux.HandleFunc("POST /api/v1/probes/{id}/rotate-key", s.withPermission(auth.PermFleetWrite, s.handleRotateKey))
	mux.HandleFunc("GET /api/v1/probes/{id}/certificates", s.withPermission(auth.PermFleetRead, s.handleListProbeCertificates))
	mux.HandleFunc("POST /api/v1/probes/{id}/certificates/register", s.withPermission(auth.PermFleetWrite, s.handleRegisterProbeCertificate))
	mux.HandleFunc("POST /api/v1/probes/{id}/certificates/issue", s.withPermission(auth.PermFleetWrite, s.handleIssueProbeCertificate))
	mux.HandleFunc("POST /api/v1/probes/{id}/update", s.withPermission(auth.PermFleetWrite, s.handleProbeUpdate))
	mux.HandleFunc("PUT /api/v1/probes/{id}/tags", s.withPermission(auth.PermFleetWrite, s.handleSetTags))
	mux.HandleFunc("POST /api/v1/probes/{id}/apply-policy/{policyId}", s.withPermission(auth.PermFleetWrite, s.handleApplyPolicy))
	mux.HandleFunc("POST /api/v1/probes/{id}/task", s.withPermission(auth.PermFleetWrite, s.handleTask))
	mux.HandleFunc("DELETE /api/v1/probes/{id}", s.withPermission(auth.PermFleetWrite, s.handleDeleteProbe))
	mux.HandleFunc("GET /api/v1/fleet/summary", s.withPermission(auth.PermFleetRead, s.handleFleetSummary))
	mux.HandleFunc("GET /api/v1/reliability/scorecard", s.withPermission(auth.PermFleetRead, s.handleReliabilityScorecard))

	// Failure drills
	if s.drillRunner != nil {
		mux.HandleFunc("GET /api/v1/reliability/drills", s.withPermission(auth.PermFleetRead, s.handleListDrills))
		mux.HandleFunc("POST /api/v1/reliability/drills/{name}/run", s.withPermission(auth.PermFleetWrite, s.handleRunDrill))
		mux.HandleFunc("GET /api/v1/reliability/drills/history", s.withPermission(auth.PermFleetRead, s.handleListDrillHistory))
	} else {
		mux.HandleFunc("GET /api/v1/reliability/drills", s.withPermission(auth.PermFleetRead, s.handleDrillsUnavailable))
		mux.HandleFunc("POST /api/v1/reliability/drills/{name}/run", s.withPermission(auth.PermFleetWrite, s.handleDrillsUnavailable))
		mux.HandleFunc("GET /api/v1/reliability/drills/history", s.withPermission(auth.PermFleetRead, s.handleDrillsUnavailable))
	}

	// Incidents
	if s.incidentStore != nil {
		mux.HandleFunc("POST /api/v1/reliability/incidents", s.withPermission(auth.PermFleetWrite, s.handleCreateIncident))
		mux.HandleFunc("GET /api/v1/reliability/incidents", s.withPermission(auth.PermFleetRead, s.handleListIncidents))
		mux.HandleFunc("GET /api/v1/reliability/incidents/{id}/export", s.withPermission(auth.PermFleetRead, s.handleExportIncident))
		mux.HandleFunc("GET /api/v1/reliability/incidents/{id}", s.withPermission(auth.PermFleetRead, s.handleGetIncident))
		mux.HandleFunc("PATCH /api/v1/reliability/incidents/{id}", s.withPermission(auth.PermFleetWrite, s.handleUpdateIncident))
		mux.HandleFunc("POST /api/v1/reliability/incidents/{id}/timeline", s.withPermission(auth.PermFleetWrite, s.handleAddTimelineEntry))
		mux.HandleFunc("DELETE /api/v1/reliability/incidents/{id}", s.withPermission(auth.PermFleetWrite, s.handleDeleteIncident))
	} else {
		mux.HandleFunc("POST /api/v1/reliability/incidents", s.withPermission(auth.PermFleetWrite, s.handleIncidentsUnavailable))
		mux.HandleFunc("GET /api/v1/reliability/incidents", s.withPermission(auth.PermFleetRead, s.handleIncidentsUnavailable))
		mux.HandleFunc("GET /api/v1/reliability/incidents/{id}/export", s.withPermission(auth.PermFleetRead, s.handleIncidentsUnavailable))
		mux.HandleFunc("GET /api/v1/reliability/incidents/{id}", s.withPermission(auth.PermFleetRead, s.handleIncidentsUnavailable))
		mux.HandleFunc("PATCH /api/v1/reliability/incidents/{id}", s.withPermission(auth.PermFleetWrite, s.handleIncidentsUnavailable))
		mux.HandleFunc("POST /api/v1/reliability/incidents/{id}/timeline", s.withPermission(auth.PermFleetWrite, s.handleIncidentsUnavailable))
		mux.HandleFunc("DELETE /api/v1/reliability/incidents/{id}", s.withPermission(auth.PermFleetWrite, s.handleIncidentsUnavailable))
	}
	mux.HandleFunc("GET /api/v1/fleet/inventory", s.withPermission(auth.PermFleetRead, s.handleFleetInventory))
	mux.HandleFunc("GET /api/v1/federation/inventory", s.withPermission(auth.PermFleetRead, s.handleFederationInventory))
	mux.HandleFunc("GET /api/v1/federation/summary", s.withPermission(auth.PermFleetRead, s.handleFederationSummary))
	mux.HandleFunc("GET /api/v1/fleet/tags", s.withPermission(auth.PermFleetRead, s.handleFleetTags))
	mux.HandleFunc("GET /api/v1/fleet/by-tag/{tag}", s.withPermission(auth.PermFleetRead, s.handleListByTag))
	mux.HandleFunc("POST /api/v1/fleet/by-tag/{tag}/command", s.withPermission(auth.PermFleetWrite, s.handleGroupCommand))
	mux.HandleFunc("POST /api/v1/fleet/cleanup", s.withPermission(auth.PermFleetWrite, s.handleFleetCleanup))

	// Registration
	mux.HandleFunc("POST /api/v1/register", api.HandleRegisterWithAudit(s.tokenStore, s.fleetMgr, s.auditRecorder(), s.logger.Named("register")))
	mux.HandleFunc("POST /api/v1/tokens", s.withPermission(auth.PermFleetWrite, api.HandleGenerateTokenWithAudit(s.tokenStore, s.auditRecorder(), s.logger.Named("tokens"))))
	mux.HandleFunc("GET /api/v1/tokens", s.withPermission(auth.PermAdmin, api.HandleListTokens(s.tokenStore)))

	// Discovery
	if s.discoveryHandlers != nil {
		mux.HandleFunc("POST /api/v1/discovery/scan", s.withPermission(auth.PermFleetWrite, s.discoveryHandlers.HandleScan))
		mux.HandleFunc("GET /api/v1/discovery/runs", s.withPermission(auth.PermFleetRead, s.discoveryHandlers.HandleListRuns))
		mux.HandleFunc("GET /api/v1/discovery/runs/{id}", s.withPermission(auth.PermFleetRead, s.discoveryHandlers.HandleGetRun))
		mux.HandleFunc("POST /api/v1/discovery/install-token", s.withPermission(auth.PermFleetWrite, s.discoveryHandlers.HandleInstallToken))
	} else {
		mux.HandleFunc("POST /api/v1/discovery/scan", s.withPermission(auth.PermFleetWrite, s.handleDiscoveryUnavailable))
		mux.HandleFunc("GET /api/v1/discovery/runs", s.withPermission(auth.PermFleetRead, s.handleDiscoveryUnavailable))
		mux.HandleFunc("GET /api/v1/discovery/runs/{id}", s.withPermission(auth.PermFleetRead, s.handleDiscoveryUnavailable))
		mux.HandleFunc("POST /api/v1/discovery/install-token", s.withPermission(auth.PermFleetWrite, s.handleDiscoveryUnavailable))
	}
	// Deployment candidate API (probe-deploys-probe lateral discovery)
	if s.candidateHandlers != nil {
		mux.HandleFunc("GET /api/v1/discovery/candidates", s.withPermission(auth.PermFleetRead, s.candidateHandlers.HandleListCandidates))
		mux.HandleFunc("GET /api/v1/discovery/candidates/{id}", s.withPermission(auth.PermFleetRead, s.candidateHandlers.HandleGetCandidate))
		mux.HandleFunc("POST /api/v1/discovery/candidates/{id}/approve", s.withPermission(auth.PermFleetWrite, s.candidateHandlers.HandleApproveCandidate))
		mux.HandleFunc("POST /api/v1/discovery/candidates/{id}/reject", s.withPermission(auth.PermFleetWrite, s.candidateHandlers.HandleRejectCandidate))
	} else {
		mux.HandleFunc("GET /api/v1/discovery/candidates", s.withPermission(auth.PermFleetRead, s.handleDiscoveryUnavailable))
		mux.HandleFunc("GET /api/v1/discovery/candidates/{id}", s.withPermission(auth.PermFleetRead, s.handleDiscoveryUnavailable))
		mux.HandleFunc("POST /api/v1/discovery/candidates/{id}/approve", s.withPermission(auth.PermFleetWrite, s.handleDiscoveryUnavailable))
		mux.HandleFunc("POST /api/v1/discovery/candidates/{id}/reject", s.withPermission(auth.PermFleetWrite, s.handleDiscoveryUnavailable))
	}

	// Compliance
	if s.complianceHandlers != nil {
		mux.HandleFunc("POST /api/v1/compliance/scan", s.withPermission(auth.PermFleetWrite, s.complianceHandlers.HandleScan))
		mux.HandleFunc("GET /api/v1/compliance/results", s.withPermission(auth.PermFleetRead, s.complianceHandlers.HandleResults))
		mux.HandleFunc("GET /api/v1/compliance/summary", s.withPermission(auth.PermFleetRead, s.complianceHandlers.HandleSummary))
		mux.HandleFunc("GET /api/v1/compliance/checks", s.withPermission(auth.PermFleetRead, s.complianceHandlers.HandleChecks))
		mux.HandleFunc("GET /api/v1/compliance/export/csv", s.withPermission(auth.PermFleetRead, s.complianceExportHandlers.HandleExportCSV))
		mux.HandleFunc("GET /api/v1/compliance/export/pdf", s.withPermission(auth.PermFleetRead, s.complianceExportHandlers.HandleExportPDF))
		mux.HandleFunc("GET /api/v1/compliance/exports", s.withPermission(auth.PermFleetRead, s.complianceExportHandlers.HandleListExports))
		mux.HandleFunc("GET /api/v1/compliance/exports/{id}", s.withPermission(auth.PermFleetRead, s.complianceExportHandlers.HandleGetExport))
	} else {
		mux.HandleFunc("POST /api/v1/compliance/scan", s.withPermission(auth.PermFleetWrite, s.handleComplianceUnavailable))
		mux.HandleFunc("GET /api/v1/compliance/results", s.withPermission(auth.PermFleetRead, s.handleComplianceUnavailable))
		mux.HandleFunc("GET /api/v1/compliance/summary", s.withPermission(auth.PermFleetRead, s.handleComplianceUnavailable))
		mux.HandleFunc("GET /api/v1/compliance/checks", s.withPermission(auth.PermFleetRead, s.handleComplianceUnavailable))
		mux.HandleFunc("GET /api/v1/compliance/export/csv", s.withPermission(auth.PermFleetRead, s.handleComplianceUnavailable))
		mux.HandleFunc("GET /api/v1/compliance/export/pdf", s.withPermission(auth.PermFleetRead, s.handleComplianceUnavailable))
		mux.HandleFunc("GET /api/v1/compliance/exports", s.withPermission(auth.PermFleetRead, s.handleComplianceUnavailable))
		mux.HandleFunc("GET /api/v1/compliance/exports/{id}", s.withPermission(auth.PermFleetRead, s.handleComplianceUnavailable))
	}

	// Metrics
	metricsCollector := metrics.NewCollector(
		s.fleetMgr,
		&hubConnectedAdapter{hub: s.hub},
		s.approvalQueue,
		s.metricsAuditCounter(),
		s.asyncJobsScheduler,
	)
	s.webhookNotifier.SetDeliveryObserver(metricsCollector)
	mux.HandleFunc("GET /api/v1/metrics", s.withPermission(auth.PermFleetRead, metricsCollector.Handler()))

	// Approvals
	mux.HandleFunc("GET /api/v1/approvals", s.withPermission(auth.PermApprovalRead, s.handleListApprovals))
	mux.HandleFunc("GET /api/v1/approvals/{id}", s.withPermission(auth.PermApprovalRead, s.handleGetApproval))
	mux.HandleFunc("POST /api/v1/approvals/{id}/decide", s.withPermission(auth.PermApprovalWrite, s.handleDecideApproval))

	// Audit
	mux.HandleFunc("GET /api/v1/audit", s.withPermission(auth.PermAuditRead, s.handleAuditLog))
	mux.HandleFunc("GET /api/v1/audit/verify", s.withPermission(auth.PermAuditRead, s.handleAuditVerify))
	mux.HandleFunc("GET /api/v1/audit/export", s.withPermission(auth.PermAuditRead, s.handleAuditExportJSONL))
	mux.HandleFunc("GET /api/v1/audit/export/csv", s.withPermission(auth.PermAuditRead, s.handleAuditExportCSV))
	mux.HandleFunc("GET /api/v1/audit/export/bundle", s.withPermission(auth.PermAuditRead, s.handleAuditEvidenceBundleExport))
	mux.HandleFunc("DELETE /api/v1/audit/purge", s.withPermission(auth.PermAdmin, s.handleAuditPurge))

	// Events SSE stream
	mux.HandleFunc("GET /api/v1/events", s.withPermission(auth.PermFleetRead, s.handleEventsSSE))

	if s.mcpServer != nil {
		mux.Handle("GET /mcp", s.mcpServer.Handler())
		mux.Handle("POST /mcp", s.mcpServer.Handler())
	}

	// MCP client API (external server connections)
	mux.HandleFunc("GET /api/v1/mcp/servers", s.withPermission(auth.PermFleetRead, s.handleListMCPServers))
	mux.HandleFunc("GET /api/v1/mcp/tools", s.withPermission(auth.PermFleetRead, s.handleListMCPTools))
	mux.HandleFunc("POST /api/v1/mcp/invoke", s.withPermission(auth.PermFleetWrite, s.handleInvokeMCPTool))

	// Commands
	mux.HandleFunc("GET /api/v1/commands/pending", s.withPermission(auth.PermCommandExec, s.handlePendingCommands))
	mux.HandleFunc("GET /api/v1/commands/{requestId}/stream", s.withPermission(auth.PermCommandExec, s.handleSSEStream))
	mux.HandleFunc("GET /api/v1/commands/{requestId}/replay", s.withPermission(auth.PermCommandExec, s.handleCommandReplay))

	// Policy templates
	mux.HandleFunc("GET /api/v1/policies", s.withPermission(auth.PermFleetRead, s.handleListPolicies))
	mux.HandleFunc("GET /api/v1/policies/{id}", s.withPermission(auth.PermFleetRead, s.handleGetPolicy))
	mux.HandleFunc("POST /api/v1/policies", s.withPermission(auth.PermFleetWrite, s.handleCreatePolicy))
	mux.HandleFunc("DELETE /api/v1/policies/{id}", s.withPermission(auth.PermFleetWrite, s.handleDeletePolicy))

	// Webhooks
	mux.HandleFunc("GET /api/v1/webhooks", s.withPermission(auth.PermWebhookManage, s.webhookNotifier.ListWebhooks))
	mux.HandleFunc("GET /api/v1/webhooks/deliveries", s.withPermission(auth.PermWebhookManage, s.webhookNotifier.ListDeliveries))
	mux.HandleFunc("POST /api/v1/webhooks", s.withPermission(auth.PermWebhookManage, s.webhookNotifier.RegisterWebhook))
	mux.HandleFunc("GET /api/v1/webhooks/{id}", s.withPermission(auth.PermWebhookManage, s.webhookNotifier.GetWebhook))
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", s.withPermission(auth.PermWebhookManage, s.webhookNotifier.DeleteWebhook))
	mux.HandleFunc("POST /api/v1/webhooks/{id}/test", s.withPermission(auth.PermWebhookManage, s.webhookNotifier.TestWebhook))

	// Alerts
	if s.alertEngine != nil {
		mux.HandleFunc("GET /api/v1/alerts", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleListRules))
		mux.HandleFunc("POST /api/v1/alerts", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleCreateRule))
		mux.HandleFunc("GET /api/v1/alerts/active", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleActiveAlerts))
		mux.HandleFunc("GET /api/v1/notification-channels", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleListChannels))
		mux.HandleFunc("POST /api/v1/notification-channels", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleCreateChannel))
		mux.HandleFunc("GET /api/v1/notification-channels/{id}", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleGetChannel))
		mux.HandleFunc("PUT /api/v1/notification-channels/{id}", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleUpdateChannel))
		mux.HandleFunc("DELETE /api/v1/notification-channels/{id}", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleDeleteChannel))
		mux.HandleFunc("POST /api/v1/notification-channels/{id}/test", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleTestChannel))
		mux.HandleFunc("GET /api/v1/alerts/{id}", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleGetRule))
		mux.HandleFunc("PUT /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleUpdateRule))
		mux.HandleFunc("DELETE /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleDeleteRule))
		mux.HandleFunc("GET /api/v1/alerts/{id}/history", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleRuleHistory))

		// Alert routing policies (additive; requires routingStore)
		if s.routingStore != nil {
			mux.HandleFunc("GET /api/v1/alerts/routing/policies", s.withPermission(auth.PermFleetRead, s.routingStore.HandleListRoutingPolicies))
			mux.HandleFunc("POST /api/v1/alerts/routing/policies", s.withPermission(auth.PermFleetWrite, s.routingStore.HandleCreateRoutingPolicy))
			mux.HandleFunc("POST /api/v1/alerts/routing/resolve", s.withPermission(auth.PermFleetRead, s.routingStore.HandleResolveRouting))
			mux.HandleFunc("GET /api/v1/alerts/routing/policies/{id}", s.withPermission(auth.PermFleetRead, s.routingStore.HandleGetRoutingPolicy))
			mux.HandleFunc("PUT /api/v1/alerts/routing/policies/{id}", s.withPermission(auth.PermFleetWrite, s.routingStore.HandleUpdateRoutingPolicy))
			mux.HandleFunc("DELETE /api/v1/alerts/routing/policies/{id}", s.withPermission(auth.PermFleetWrite, s.routingStore.HandleDeleteRoutingPolicy))
			mux.HandleFunc("GET /api/v1/alerts/escalation/policies", s.withPermission(auth.PermFleetRead, s.routingStore.HandleListEscalationPolicies))
			mux.HandleFunc("POST /api/v1/alerts/escalation/policies", s.withPermission(auth.PermFleetWrite, s.routingStore.HandleCreateEscalationPolicy))
			mux.HandleFunc("GET /api/v1/alerts/escalation/policies/{id}", s.withPermission(auth.PermFleetRead, s.routingStore.HandleGetEscalationPolicy))
			mux.HandleFunc("PUT /api/v1/alerts/escalation/policies/{id}", s.withPermission(auth.PermFleetWrite, s.routingStore.HandleUpdateEscalationPolicy))
			mux.HandleFunc("DELETE /api/v1/alerts/escalation/policies/{id}", s.withPermission(auth.PermFleetWrite, s.routingStore.HandleDeleteEscalationPolicy))
		}
	} else {
		mux.HandleFunc("GET /api/v1/alerts", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("POST /api/v1/alerts", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/alerts/active", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/notification-channels", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("POST /api/v1/notification-channels", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/notification-channels/{id}", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("PUT /api/v1/notification-channels/{id}", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("DELETE /api/v1/notification-channels/{id}", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("POST /api/v1/notification-channels/{id}/test", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/alerts/{id}", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("PUT /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("DELETE /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/alerts/{id}/history", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
	}

	// Scheduled jobs
	if s.jobsHandler != nil {
		mux.HandleFunc("GET /api/v1/jobs", s.withPermission(auth.PermFleetRead, s.withWorkspaceScope(s.jobsHandler.HandleListJobs)))
		mux.HandleFunc("GET /api/v1/jobs/runs", s.withPermission(auth.PermFleetRead, s.withWorkspaceScope(s.jobsHandler.HandleListAllRuns)))
		mux.HandleFunc("POST /api/v1/jobs", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleCreateJob)))
		mux.HandleFunc("GET /api/v1/jobs/{id}", s.withPermission(auth.PermFleetRead, s.withWorkspaceScope(s.jobsHandler.HandleGetJob)))
		mux.HandleFunc("PUT /api/v1/jobs/{id}", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleUpdateJob)))
		mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleDeleteJob)))
		mux.HandleFunc("POST /api/v1/jobs/{id}/run", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleRunJob)))
		mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleCancelJob)))
		mux.HandleFunc("GET /api/v1/jobs/{id}/runs", s.withPermission(auth.PermFleetRead, s.withWorkspaceScope(s.jobsHandler.HandleListRuns)))
		mux.HandleFunc("POST /api/v1/jobs/{id}/runs/{runId}/cancel", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleCancelRun)))
		mux.HandleFunc("POST /api/v1/jobs/{id}/runs/{runId}/retry", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleRetryRun)))
		mux.HandleFunc("POST /api/v1/jobs/{id}/enable", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleEnableJob)))
		mux.HandleFunc("POST /api/v1/jobs/{id}/disable", s.withPermission(auth.PermFleetWrite, s.withWorkspaceScope(s.jobsHandler.HandleDisableJob)))
	} else {
		mux.HandleFunc("GET /api/v1/jobs", s.withPermission(auth.PermFleetRead, s.handleJobsUnavailable))
		mux.HandleFunc("GET /api/v1/jobs/runs", s.withPermission(auth.PermFleetRead, s.handleJobsUnavailable))
		mux.HandleFunc("POST /api/v1/jobs", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("GET /api/v1/jobs/{id}", s.withPermission(auth.PermFleetRead, s.handleJobsUnavailable))
		mux.HandleFunc("PUT /api/v1/jobs/{id}", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("POST /api/v1/jobs/{id}/run", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("GET /api/v1/jobs/{id}/runs", s.withPermission(auth.PermFleetRead, s.handleJobsUnavailable))
		mux.HandleFunc("POST /api/v1/jobs/{id}/runs/{runId}/cancel", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("POST /api/v1/jobs/{id}/runs/{runId}/retry", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("POST /api/v1/jobs/{id}/enable", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
		mux.HandleFunc("POST /api/v1/jobs/{id}/disable", s.withPermission(auth.PermFleetWrite, s.handleJobsUnavailable))
	}

	// Async job approval — always registered (approval works regardless of scheduled jobs)
	mux.HandleFunc("POST /api/v1/jobs/{id}/approve", s.withPermission(auth.PermApprovalWrite, s.handleApproveAsyncJob))
	mux.HandleFunc("POST /api/v1/jobs/{id}/reject", s.withPermission(auth.PermApprovalWrite, s.handleRejectAsyncJob))

	// Runner manager + ephemeral run token contract.
	mux.HandleFunc("POST /api/v1/runners", s.withPermission(auth.PermCommandExec, s.handleCreateRunner))
	mux.HandleFunc("POST /api/v1/runners/{id}/start", s.withPermission(auth.PermCommandExec, s.handleStartRunner))
	mux.HandleFunc("POST /api/v1/runners/{id}/stop", s.withPermission(auth.PermCommandExec, s.handleStopRunner))
	mux.HandleFunc("DELETE /api/v1/runners/{id}", s.withPermission(auth.PermCommandExec, s.handleDestroyRunner))
	mux.HandleFunc("POST /api/v1/runs", s.withPermission(auth.PermCommandExec, s.handleIssueRunToken))
	mux.HandleFunc("POST /api/v1/runs/{id}/artifacts/presign", s.withPermission(auth.PermCommandExec, s.handlePresignRunnerArtifact))
	mux.HandleFunc("POST /api/v1/runs/{id}/provider-proxy", s.withPermission(auth.PermCommandExec, s.handleProviderProxy))

	// Runner artifact transfers use presigned URLs and do not require API keys.
	mux.HandleFunc("PUT /artifacts/runs/{id}/{path...}", s.handleUploadRunnerArtifact)
	mux.HandleFunc("GET /artifacts/runs/{id}/{path...}", s.handleDownloadRunnerArtifact)

	// Permission matrix — public endpoint, no auth required
	mux.HandleFunc("GET /api/v1/auth/permissions", s.handlePermissionMatrix)

	// Roles — list all roles (public), manage custom roles (admin only)
	mux.HandleFunc("GET /api/v1/roles", s.handleListRoles)
	mux.HandleFunc("POST /api/v1/roles", s.withPermission(auth.PermAdmin, s.handleCreateRole))
	mux.HandleFunc("DELETE /api/v1/roles/{name}", s.withPermission(auth.PermAdmin, s.handleDeleteRole))

	// User role assignment (admin only)
	mux.HandleFunc("GET /api/v1/users/{id}/role", s.withPermission(auth.PermAdmin, s.handleGetUserRole))
	mux.HandleFunc("PUT /api/v1/users/{id}/role", s.withPermission(auth.PermAdmin, s.handlePutUserRole))

	// Multi-tenant management
	mux.HandleFunc("POST /api/v1/tenants", s.withPermission(auth.PermAdmin, s.handleCreateTenant))
	mux.HandleFunc("GET /api/v1/tenants", s.withPermission(auth.PermFleetRead, s.withTenantScope(s.handleListTenants)))
	mux.HandleFunc("GET /api/v1/tenants/{id}", s.withPermission(auth.PermFleetRead, s.handleGetTenant))
	mux.HandleFunc("PATCH /api/v1/tenants/{id}", s.withPermission(auth.PermAdmin, s.handleUpdateTenant))
	mux.HandleFunc("DELETE /api/v1/tenants/{id}", s.withPermission(auth.PermAdmin, s.handleDeleteTenant))
	mux.HandleFunc("PUT /api/v1/users/{id}/tenants", s.withPermission(auth.PermAdmin, s.handleAssignUserTenants))

	// Sandbox lifecycle API
	if s.sandboxHandler != nil {
		mux.HandleFunc("POST /api/v1/sandboxes", s.withPermission(auth.PermFleetWrite, s.sandboxHandler.HandleCreate))
		mux.HandleFunc("GET /api/v1/sandboxes", s.withPermission(auth.PermFleetRead, s.sandboxHandler.HandleList))
		mux.HandleFunc("GET /api/v1/sandboxes/{id}", s.withPermission(auth.PermFleetRead, s.sandboxHandler.HandleGet))
		mux.HandleFunc("DELETE /api/v1/sandboxes/{id}", s.withPermission(auth.PermFleetWrite, s.sandboxHandler.HandleDestroy))
		mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", s.withPermission(auth.PermFleetWrite, s.sandboxHandler.HandleTransition))
		// Sandbox task sub-routes (task execution layer)
		if s.sandboxTaskHandler != nil {
			mux.HandleFunc("POST /api/v1/sandboxes/{id}/tasks", s.withPermission(auth.PermFleetWrite, s.sandboxTaskHandler.HandleCreateTask))
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/tasks", s.withPermission(auth.PermFleetRead, s.sandboxTaskHandler.HandleListTasks))
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/tasks/{taskId}", s.withPermission(auth.PermFleetRead, s.sandboxTaskHandler.HandleGetTask))
			mux.HandleFunc("POST /api/v1/sandboxes/{id}/tasks/{taskId}/cancel", s.withPermission(auth.PermFleetWrite, s.sandboxTaskHandler.HandleCancelTask))
		}
		// Sandbox output streaming routes
		if s.sandboxStreamHandler != nil {
			mux.HandleFunc("POST /api/v1/sandboxes/{id}/output", s.sandboxStreamHandler.HandleIngestOutput)
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/output", s.withPermission(auth.PermFleetRead, s.sandboxStreamHandler.HandleGetOutput))
			mux.HandleFunc("GET /ws/sandboxes/{id}/stream", s.sandboxStreamHandler.HandleStreamOutput)
		}
		// Sandbox artifact routes
		if s.sandboxArtifactHandler != nil {
			mux.HandleFunc("POST /api/v1/sandboxes/{id}/artifacts", s.sandboxArtifactHandler.HandleUploadArtifact)
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/artifacts", s.withPermission(auth.PermFleetRead, s.sandboxArtifactHandler.HandleListArtifacts))
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/artifacts/{artifactId}", s.withPermission(auth.PermFleetRead, s.sandboxArtifactHandler.HandleGetArtifact))
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/artifacts/{artifactId}/content", s.withPermission(auth.PermFleetRead, s.sandboxArtifactHandler.HandleDownloadArtifact))
		}
		// Sandbox replay routes
		if s.sandboxReplayHandler != nil {
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/replay", s.withPermission(auth.PermFleetRead, s.sandboxReplayHandler.HandleReplay))
			mux.HandleFunc("GET /api/v1/sandboxes/{id}/replay/summary", s.withPermission(auth.PermFleetRead, s.sandboxReplayHandler.HandleReplaySummary))
		}
	} else {
		mux.HandleFunc("POST /api/v1/sandboxes", s.withPermission(auth.PermFleetWrite, s.handleSandboxUnavailable))
		mux.HandleFunc("GET /api/v1/sandboxes", s.withPermission(auth.PermFleetRead, s.handleSandboxUnavailable))
		mux.HandleFunc("GET /api/v1/sandboxes/{id}", s.withPermission(auth.PermFleetRead, s.handleSandboxUnavailable))
		mux.HandleFunc("DELETE /api/v1/sandboxes/{id}", s.withPermission(auth.PermFleetWrite, s.handleSandboxUnavailable))
		mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", s.withPermission(auth.PermFleetWrite, s.handleSandboxUnavailable))
	}

	// Auth (optional)
	if s.authStore != nil {
		mux.HandleFunc("GET /api/v1/auth/keys", s.withPermission(auth.PermAdmin, auth.HandleListKeys(s.authStore)))
		mux.HandleFunc("POST /api/v1/auth/keys", s.withPermission(auth.PermAdmin, auth.HandleCreateKey(s.authStore)))
		mux.HandleFunc("DELETE /api/v1/auth/keys/{id}", s.withPermission(auth.PermAdmin, auth.HandleDeleteKey(s.authStore)))
	}

	// Chat API
	if s.chatStore != nil {
		mux.HandleFunc("GET /api/v1/probes/{id}/chat", s.withPermission(auth.PermFleetRead, s.chatStore.HandleGetMessages))
		mux.HandleFunc("POST /api/v1/probes/{id}/chat", s.withPermission(auth.PermFleetRead, s.chatStore.HandleSendMessage))
		mux.HandleFunc("DELETE /api/v1/probes/{id}/chat", s.withPermission(auth.PermFleetRead, s.chatStore.HandleClearChat))
		mux.HandleFunc("GET /ws/chat", s.withPermission(auth.PermFleetRead, s.chatStore.HandleChatWS))
	} else {
		mux.HandleFunc("GET /api/v1/probes/{id}/chat", s.withPermission(auth.PermFleetRead, s.chatMgr.HandleGetMessages))
		mux.HandleFunc("POST /api/v1/probes/{id}/chat", s.withPermission(auth.PermFleetRead, s.chatMgr.HandleSendMessage))
		mux.HandleFunc("DELETE /api/v1/probes/{id}/chat", s.withPermission(auth.PermFleetRead, s.chatMgr.HandleClearChat))
		mux.HandleFunc("GET /ws/chat", s.withPermission(auth.PermFleetRead, s.chatMgr.HandleChatWS))
	}
	mux.HandleFunc("GET /api/v1/fleet/chat", s.withPermission(auth.PermFleetRead, s.handleFleetGetMessages))
	mux.HandleFunc("POST /api/v1/fleet/chat", s.withPermission(auth.PermFleetRead, s.handleFleetSendMessage))
	mux.HandleFunc("GET /ws/fleet-chat", s.withPermission(auth.PermFleetRead, s.handleFleetChatWS))

	// Model Dock API
	if s.modelDockHandlers != nil {
		mux.HandleFunc("GET /api/v1/model-profiles", s.withPermission(auth.PermFleetRead, s.modelDockHandlers.HandleListProfiles))
		mux.HandleFunc("POST /api/v1/model-profiles", s.withPermission(auth.PermFleetWrite, s.modelDockHandlers.HandleCreateProfile))
		mux.HandleFunc("PUT /api/v1/model-profiles/{id}", s.withPermission(auth.PermFleetWrite, s.modelDockHandlers.HandleUpdateProfile))
		mux.HandleFunc("DELETE /api/v1/model-profiles/{id}", s.withPermission(auth.PermFleetWrite, s.modelDockHandlers.HandleDeleteProfile))
		mux.HandleFunc("POST /api/v1/model-profiles/{id}/activate", s.withPermission(auth.PermFleetWrite, s.modelDockHandlers.HandleActivateProfile))
		mux.HandleFunc("GET /api/v1/model-profiles/active", s.withPermission(auth.PermFleetRead, s.modelDockHandlers.HandleGetActiveProfile))
		mux.HandleFunc("GET /api/v1/model-usage", s.withPermission(auth.PermFleetRead, s.modelDockHandlers.HandleGetUsage))
	} else {
		mux.HandleFunc("GET /api/v1/model-profiles", s.withPermission(auth.PermFleetRead, s.handleModelDockUnavailable))
		mux.HandleFunc("POST /api/v1/model-profiles", s.withPermission(auth.PermFleetWrite, s.handleModelDockUnavailable))
		mux.HandleFunc("PUT /api/v1/model-profiles/{id}", s.withPermission(auth.PermFleetWrite, s.handleModelDockUnavailable))
		mux.HandleFunc("DELETE /api/v1/model-profiles/{id}", s.withPermission(auth.PermFleetWrite, s.handleModelDockUnavailable))
		mux.HandleFunc("POST /api/v1/model-profiles/{id}/activate", s.withPermission(auth.PermFleetWrite, s.handleModelDockUnavailable))
		mux.HandleFunc("GET /api/v1/model-profiles/active", s.withPermission(auth.PermFleetRead, s.handleModelDockUnavailable))
		mux.HandleFunc("GET /api/v1/model-usage", s.withPermission(auth.PermFleetRead, s.handleModelDockUnavailable))
	}

	// Cloud Connectors API
	if s.cloudConnectorHandlers != nil {
		mux.HandleFunc("GET /api/v1/cloud/connectors", s.withPermission(auth.PermFleetRead, s.cloudConnectorHandlers.HandleListConnectors))
		mux.HandleFunc("POST /api/v1/cloud/connectors", s.withPermission(auth.PermFleetWrite, s.cloudConnectorHandlers.HandleCreateConnector))
		mux.HandleFunc("PUT /api/v1/cloud/connectors/{id}", s.withPermission(auth.PermFleetWrite, s.cloudConnectorHandlers.HandleUpdateConnector))
		mux.HandleFunc("DELETE /api/v1/cloud/connectors/{id}", s.withPermission(auth.PermFleetWrite, s.cloudConnectorHandlers.HandleDeleteConnector))
		mux.HandleFunc("POST /api/v1/cloud/connectors/{id}/scan", s.withPermission(auth.PermFleetWrite, s.cloudConnectorHandlers.HandleScanConnector))
		mux.HandleFunc("GET /api/v1/cloud/assets", s.withPermission(auth.PermFleetRead, s.cloudConnectorHandlers.HandleListAssets))
	} else {
		mux.HandleFunc("GET /api/v1/cloud/connectors", s.withPermission(auth.PermFleetRead, s.handleCloudConnectorsUnavailable))
		mux.HandleFunc("POST /api/v1/cloud/connectors", s.withPermission(auth.PermFleetWrite, s.handleCloudConnectorsUnavailable))
		mux.HandleFunc("PUT /api/v1/cloud/connectors/{id}", s.withPermission(auth.PermFleetWrite, s.handleCloudConnectorsUnavailable))
		mux.HandleFunc("DELETE /api/v1/cloud/connectors/{id}", s.withPermission(auth.PermFleetWrite, s.handleCloudConnectorsUnavailable))
		mux.HandleFunc("POST /api/v1/cloud/connectors/{id}/scan", s.withPermission(auth.PermFleetWrite, s.handleCloudConnectorsUnavailable))
		mux.HandleFunc("GET /api/v1/cloud/assets", s.withPermission(auth.PermFleetRead, s.handleCloudConnectorsUnavailable))
	}

	// Automation Packs API
	if s.automationPackHandlers != nil {
		mux.HandleFunc("GET /api/v1/automation-packs", s.withPermission(auth.PermFleetRead, s.automationPackHandlers.HandleListDefinitions))
		mux.HandleFunc("POST /api/v1/automation-packs", s.withPermission(auth.PermFleetWrite, s.automationPackHandlers.HandleCreateDefinition))
		mux.HandleFunc("GET /api/v1/automation-packs/{id}", s.withPermission(auth.PermFleetRead, s.automationPackHandlers.HandleGetDefinition))
		mux.HandleFunc("POST /api/v1/automation-packs/dry-run", s.withPermission(auth.PermFleetWrite, s.automationPackHandlers.HandleDryRunDefinition))
		mux.HandleFunc("POST /api/v1/automation-packs/{id}/executions", s.withPermission(auth.PermFleetWrite, s.automationPackHandlers.HandleStartExecution))
		mux.HandleFunc("GET /api/v1/automation-packs/executions/{executionID}", s.withPermission(auth.PermFleetRead, s.automationPackHandlers.HandleGetExecution))
		mux.HandleFunc("GET /api/v1/automation-packs/executions/{executionID}/timeline", s.withPermission(auth.PermFleetRead, s.automationPackHandlers.HandleGetExecutionTimeline))
		mux.HandleFunc("GET /api/v1/automation-packs/executions/{executionID}/artifacts", s.withPermission(auth.PermFleetRead, s.automationPackHandlers.HandleGetExecutionArtifacts))
	} else {
		mux.HandleFunc("GET /api/v1/automation-packs", s.withPermission(auth.PermFleetRead, s.handleAutomationPacksUnavailable))
		mux.HandleFunc("POST /api/v1/automation-packs", s.withPermission(auth.PermFleetWrite, s.handleAutomationPacksUnavailable))
		mux.HandleFunc("GET /api/v1/automation-packs/{id}", s.withPermission(auth.PermFleetRead, s.handleAutomationPacksUnavailable))
		mux.HandleFunc("POST /api/v1/automation-packs/dry-run", s.withPermission(auth.PermFleetWrite, s.handleAutomationPacksUnavailable))
		mux.HandleFunc("POST /api/v1/automation-packs/{id}/executions", s.withPermission(auth.PermFleetWrite, s.handleAutomationPacksUnavailable))
		mux.HandleFunc("GET /api/v1/automation-packs/executions/{executionID}", s.withPermission(auth.PermFleetRead, s.handleAutomationPacksUnavailable))
		mux.HandleFunc("GET /api/v1/automation-packs/executions/{executionID}/timeline", s.withPermission(auth.PermFleetRead, s.handleAutomationPacksUnavailable))
		mux.HandleFunc("GET /api/v1/automation-packs/executions/{executionID}/artifacts", s.withPermission(auth.PermFleetRead, s.handleAutomationPacksUnavailable))
	}

	// Kubeflow API (read endpoints + guarded mutations)
	if s.kubeflowHandlers != nil {
		mux.HandleFunc("GET /api/v1/kubeflow/status", s.withPermission(auth.PermFleetRead, s.kubeflowHandlers.HandleStatus))
		mux.HandleFunc("GET /api/v1/kubeflow/inventory", s.withPermission(auth.PermFleetRead, s.kubeflowHandlers.HandleInventory))
		mux.HandleFunc("GET /api/v1/kubeflow/runs/{name}/status", s.withPermission(auth.PermFleetRead, s.handleKubeflowRunStatus))
		mux.HandleFunc("POST /api/v1/kubeflow/actions/refresh", s.withPermission(auth.PermFleetWrite, s.kubeflowHandlers.HandleRefresh))
		mux.HandleFunc("POST /api/v1/kubeflow/runs/submit", s.withPermission(auth.PermFleetWrite, s.handleKubeflowSubmitRun))
		mux.HandleFunc("POST /api/v1/kubeflow/runs/{name}/cancel", s.withPermission(auth.PermFleetWrite, s.handleKubeflowCancelRun))
	} else {
		mux.HandleFunc("GET /api/v1/kubeflow/status", s.withPermission(auth.PermFleetRead, s.handleKubeflowUnavailable))
		mux.HandleFunc("GET /api/v1/kubeflow/inventory", s.withPermission(auth.PermFleetRead, s.handleKubeflowUnavailable))
		mux.HandleFunc("GET /api/v1/kubeflow/runs/{name}/status", s.withPermission(auth.PermFleetRead, s.handleKubeflowUnavailable))
		mux.HandleFunc("POST /api/v1/kubeflow/actions/refresh", s.withPermission(auth.PermFleetWrite, s.handleKubeflowUnavailable))
		mux.HandleFunc("POST /api/v1/kubeflow/runs/submit", s.withPermission(auth.PermFleetWrite, s.handleKubeflowUnavailable))
		mux.HandleFunc("POST /api/v1/kubeflow/runs/{name}/cancel", s.withPermission(auth.PermFleetWrite, s.handleKubeflowUnavailable))
	}

	// Grafana API (read-only capacity snapshot)
	if s.grafanaHandlers != nil {
		mux.HandleFunc("GET /api/v1/grafana/status", s.withPermission(auth.PermFleetRead, s.grafanaHandlers.HandleStatus))
		mux.HandleFunc("GET /api/v1/grafana/snapshot", s.withPermission(auth.PermFleetRead, s.grafanaHandlers.HandleSnapshot))
	} else {
		mux.HandleFunc("GET /api/v1/grafana/status", s.withPermission(auth.PermFleetRead, s.handleGrafanaUnavailable))
		mux.HandleFunc("GET /api/v1/grafana/snapshot", s.withPermission(auth.PermFleetRead, s.handleGrafanaUnavailable))
	}

	// Network Devices API
	if s.networkDeviceHandlers != nil {
		mux.HandleFunc("GET /api/v1/network/devices", s.withPermission(auth.PermFleetRead, s.networkDeviceHandlers.HandleListDevices))
		mux.HandleFunc("POST /api/v1/network/devices", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleCreateDevice))
		mux.HandleFunc("GET /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetRead, s.networkDeviceHandlers.HandleGetDevice))
		mux.HandleFunc("PUT /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleUpdateDevice))
		mux.HandleFunc("DELETE /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleDeleteDevice))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/test", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleTestDevice))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/inventory", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleInventoryDevice))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/command", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleCommandDevice))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/scan", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleScanDevice))
		mux.HandleFunc("GET /api/v1/network/devices/{id}/inventory", s.withPermission(auth.PermFleetRead, s.networkDeviceHandlers.HandleGetInventory))
		mux.HandleFunc("POST /api/v1/network-devices/{id}/command", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleCommandDevice))
		mux.HandleFunc("POST /api/v1/network-devices/{id}/scan", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleScanDevice))
		mux.HandleFunc("GET /api/v1/network-devices/{id}/inventory", s.withPermission(auth.PermFleetRead, s.networkDeviceHandlers.HandleGetInventory))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/enrich", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleEnrichDevice))
		mux.HandleFunc("GET /api/v1/network/devices/{id}/interfaces", s.withPermission(auth.PermFleetRead, s.networkDeviceHandlers.HandleGetInterfaces))
		mux.HandleFunc("POST /api/v1/network-devices/{id}/enrich", s.withPermission(auth.PermFleetWrite, s.networkDeviceHandlers.HandleEnrichDevice))
		mux.HandleFunc("GET /api/v1/network-devices/{id}/interfaces", s.withPermission(auth.PermFleetRead, s.networkDeviceHandlers.HandleGetInterfaces))
	} else {
		mux.HandleFunc("GET /api/v1/network/devices", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("GET /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("PUT /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("DELETE /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/test", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/inventory", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/command", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/scan", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("GET /api/v1/network/devices/{id}/inventory", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network-devices/{id}/command", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network-devices/{id}/scan", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("GET /api/v1/network-devices/{id}/inventory", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/enrich", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("GET /api/v1/network/devices/{id}/interfaces", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network-devices/{id}/enrich", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("GET /api/v1/network-devices/{id}/interfaces", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
	}

	// Binary download + install script
	mux.HandleFunc("GET /download/{filename}", s.handleDownload)
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)

	// Static assets
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join("web", "static")))))

	// Landing page (testing)
	mux.Handle("GET /site/", http.StripPrefix("/site/", http.FileServer(http.Dir(filepath.Join("web", "site")))))

	// Dashboard
	mux.HandleFunc("GET /dashboard", s.handleDashboardPage)
	mux.HandleFunc("GET /api/v1/dashboard", s.withPermission(auth.PermFleetRead, s.handleDashboardAPI))

	// Web UI pages — / redirects to /dashboard when templates are loaded
	mux.HandleFunc("GET /", s.handleRootPage)
	mux.HandleFunc("GET /fleet", s.handleFleetPage)
	mux.HandleFunc("GET /federation", s.handleFederationPage)
	mux.HandleFunc("GET /fleet/chat", s.handleFleetChatPage)
	mux.HandleFunc("GET /probe/{id}", s.handleProbeDetailPage)
	mux.HandleFunc("GET /probe/{id}/chat", s.handleChatPage)
	mux.HandleFunc("GET /approvals", s.handleApprovalsPage)
	mux.HandleFunc("GET /audit", s.handleAuditPage)
	mux.HandleFunc("GET /alerts", s.handleAlertsPage)
	mux.HandleFunc("GET /model-dock", s.handleModelDockPage)
	mux.HandleFunc("GET /cloud-connectors", s.handleCloudConnectorsPage)
	mux.HandleFunc("GET /network-devices", s.handleNetworkDevicesPage)
	mux.HandleFunc("GET /discovery", s.handleDiscoveryPage)
	mux.HandleFunc("GET /jobs", s.handleJobsPage)
	mux.HandleFunc("GET /compliance", s.handleCompliancePage)
	mux.HandleFunc("GET /sandboxes", s.handleSandboxesPage)
	mux.HandleFunc("GET /sandboxes/{id}", s.handleSandboxDetailPage)

	// WebSocket for probes
	mux.HandleFunc("GET /ws/probe", s.hub.HandleProbeWS)
}

// handleRootPage redirects authenticated users to the dashboard. When
// templates aren't loaded it falls back to the legacy fleet page.
func (s *Server) handleRootPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if s.pages != nil {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	// Fallback: no templates → serve inline fleet page.
	s.handleFleetPage(w, r)
}

func (s *Server) withPermission(perm auth.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requirePermission(w, r, perm) {
			return
		}
		next(w, r)
	}
}

func (s *Server) requirePermission(w http.ResponseWriter, r *http.Request, perm auth.Permission) bool {
	if s.authStore == nil && s.sessionValidator == nil {
		return true
	}

	if !auth.IsAuthenticated(r.Context()) {
		s.recordAuthorizationDenied(r, perm, "authentication_required")
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return false
	}

	if !auth.HasPermissionFromContext(r.Context(), perm) {
		s.recordAuthorizationDenied(r, perm, "insufficient_permissions")
		writeJSONError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("insufficient permissions (required: %s)", perm))
		return false
	}

	return true
}

func (s *Server) currentTemplateUser(r *http.Request) *TemplateUser {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return nil
	}

	permissions := make(map[auth.Permission]struct{}, len(user.Permissions))
	for _, perm := range user.Permissions {
		permissions[perm] = struct{}{}
	}

	return &TemplateUser{
		Username:    user.Username,
		Role:        user.Role,
		Permissions: permissions,
	}
}

func (s *Server) recordAuthorizationDenied(r *http.Request, perm auth.Permission, reason string) {
	if s.auditStore == nil {
		return
	}

	actor := "anonymous"
	if user := auth.UserFromContext(r.Context()); user != nil {
		if user.Username != "" {
			actor = user.Username
		} else if user.ID != "" {
			actor = user.ID
		}
	} else if key := auth.FromContext(r.Context()); key != nil {
		if key.Name != "" {
			actor = key.Name
		} else if key.ID != "" {
			actor = key.ID
		}
	}

	detail := map[string]string{
		"method":              r.Method,
		"path":                r.URL.Path,
		"required_permission": string(perm),
		"reason":              reason,
	}

	s.recordAudit(audit.Event{
		Timestamp: time.Now().UTC(),
		Type:      audit.EventAuthorizationDenied,
		Actor:     actor,
		ProbeID:   "",
		Summary:   fmt.Sprintf("authorization denied for %s %s", r.Method, r.URL.Path),
		Detail:    detail,
	})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if s.userStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not enabled")
		return
	}
	users, err := s.userStore.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"users": users, "total": len(users)})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.userStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not enabled")
		return
	}
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if body.Username == "" || body.Password == "" || body.Role == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "username, password, and role required")
		return
	}
	user, err := s.userStore.Create(body.Username, body.DisplayName, body.Password, body.Role)
	if err != nil {
		writeJSONError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if s.userStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "auth not enabled")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "user id required")
		return
	}
	if err := s.userStore.Delete(id); err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// ── Health / Version ─────────────────────────────────────────

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"version": Version, "commit": Commit, "date": Date,
	})
}

// ── Fleet API ────────────────────────────────────────────────

func (s *Server) handleListProbes(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.probesForRequest(r))
}

func (s *Server) handleGetProbe(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.probeForRequest(r, id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ps)
}

func (s *Server) handleCreateProbe(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}

	var body struct {
		ID       string   `json:"id"`
		Type     string   `json:"type"`
		Hostname string   `json:"hostname"`
		OS       string   `json:"os"`
		Arch     string   `json:"arch"`
		Tags     []string `json:"tags"`
		Remote   struct {
			Host       string `json:"host"`
			Port       int    `json:"port"`
			Username   string `json:"username"`
			AuthMode   string `json:"auth_mode"`
			Password   string `json:"password"`
			PrivateKey string `json:"private_key"`
		} `json:"remote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	if !strings.EqualFold(strings.TrimSpace(body.Type), fleet.ProbeTypeRemote) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "only type=remote is supported on this endpoint")
		return
	}

	probeID := strings.TrimSpace(body.ID)
	if probeID == "" {
		probeID = "rpr-" + uuid.New().String()[:8]
	}

	spec := fleet.RemoteProbeRegistration{
		ID:       probeID,
		Hostname: strings.TrimSpace(body.Hostname),
		OS:       strings.TrimSpace(body.OS),
		Arch:     strings.TrimSpace(body.Arch),
		Tags:     body.Tags,
		Remote: fleet.RemoteProbeConfig{
			Host:     strings.TrimSpace(body.Remote.Host),
			Port:     body.Remote.Port,
			Username: strings.TrimSpace(body.Remote.Username),
			AuthMode: strings.TrimSpace(body.Remote.AuthMode),
		},
		Credentials: fleet.RemoteProbeCredentials{
			Password:   strings.TrimSpace(body.Remote.Password),
			PrivateKey: strings.TrimSpace(body.Remote.PrivateKey),
		},
	}

	if scope := tenant.ScopeFromContext(r.Context()); !scope.IsAdmin && len(scope.TenantIDs) == 1 {
		spec.TenantID = scope.TenantIDs[0]
	}

	ps, err := s.fleetMgr.RegisterRemote(spec)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	s.emitAudit(audit.EventProbeRegistered, ps.ID, "api", fmt.Sprintf("Remote probe registered: %s", ps.Hostname))
	if s.remoteScanner != nil {
		go s.remoteScanner.ScanProbe(context.Background(), ps.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(ps)
}

func (s *Server) handleProbeHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.probeForRequest(r, id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}
	health := ps.Health
	if health == nil {
		health = &fleet.HealthScore{Score: 0, Status: "unknown", Warnings: []string{"no heartbeat data yet"}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(health)
}

func (s *Server) handleDispatchCommand(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	var body struct {
		protocol.CommandPayload
		BreakglassReason string `json:"breakglass_reason,omitempty"`
		BreakglassToken  string `json:"breakglass_token,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	cmd := body.CommandPayload
	wantWait := r.URL.Query().Get("wait") == "true" || r.URL.Query().Get("wait") == "1"
	wantStream := r.URL.Query().Get("stream") == "true" || r.URL.Query().Get("stream") == "1"

	invokeInput := corecommanddispatch.AssembleCommandInvokeHTTP(id, cmd, wantWait, wantStream)
	if invokeInput == nil {
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "command dispatch failed")
		return
	}
	cmd = invokeInput.Command

	decision := s.approvalCore.EvaluateCommandPolicyForProbe(r.Context(), id, &cmd, ps.PolicyLevel)
	w.Header().Set("X-Legator-Policy-Decision", string(decision.Outcome))
	w.Header().Set("X-Legator-Execution-Lane", string(decision.Lane))
	w.Header().Set("X-Legator-Gate-Outcome", string(decision.GateOutcome))
	w.Header().Set("X-Legator-Reason-Code", decision.ReasonCode)
	w.Header().Set("X-Legator-Risk-Tier", strconv.Itoa(decision.RiskTier))
	s.appendCommandStreamMarker(cmd.RequestID, cmdtracker.StreamEventPolicy, "policy_decision", map[string]any{
		"outcome":      decision.Outcome,
		"lane":         decision.Lane,
		"gate_outcome": decision.GateOutcome,
		"reason_code":  decision.ReasonCode,
		"risk_tier":    decision.RiskTier,
		"risk_level":   decision.RiskLevel,
	})

	breakglass := controlpolicy.ResolveBreakglassConfirmation(body.BreakglassReason, body.BreakglassToken)
	requiresBreakglass := controlpolicy.RequiresBreakglassConfirmation(decision.Classification.Category, decision.Lane)
	if s.cfg.SandboxEnforcement && requiresBreakglass {
		if !breakglass.Confirmed {
			reasonCode := "sandbox_enforcement.breakglass_required"
			w.Header().Set("X-Legator-Reason-Code", reasonCode)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "blocked",
				"code":        "sandbox_enforcement",
				"reason_code": reasonCode,
				"lane":        decision.Lane,
				"message":     "Sandbox enforcement blocked host-direct mutation dispatch. Supply breakglass_reason or breakglass_token.",
			})
			return
		}
		if breakglass.Method == controlpolicy.BreakglassConfirmReasonField && !controlpolicy.BreakglassReasonAllowed(breakglass.Reason, decision.Policy.Breakglass.AllowedReasons) {
			reasonCode := "sandbox_enforcement.breakglass_reason_not_allowed"
			w.Header().Set("X-Legator-Reason-Code", reasonCode)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "blocked",
				"code":        "sandbox_enforcement",
				"reason_code": reasonCode,
				"lane":        decision.Lane,
				"message":     "Breakglass reason is not allowed by the applied policy.",
			})
			return
		}

		actor := "api"
		if user := auth.UserFromContext(r.Context()); user != nil {
			if user.Username != "" {
				actor = user.Username
			} else if user.ID != "" {
				actor = user.ID
			}
		} else if key := auth.FromContext(r.Context()); key != nil {
			if key.Name != "" {
				actor = key.Name
			} else if key.ID != "" {
				actor = key.ID
			}
		}

		s.recordAudit(audit.Event{
			Type:    audit.EventBreakglassCommand,
			ProbeID: id,
			Actor:   actor,
			Summary: fmt.Sprintf("Breakglass confirmed for command: %s", cmd.Command),
			Detail: map[string]any{
				"request_id": cmd.RequestID,
				"command":    cmd.Command,
				"lane":       decision.Lane,
				"reason":     breakglass.Reason,
				"method":     breakglass.Method,
			},
		})
		s.appendCommandStreamMarker(cmd.RequestID, cmdtracker.StreamEventPolicy, "breakglass_confirmed", map[string]any{
			"lane":   decision.Lane,
			"reason": breakglass.Reason,
			"method": breakglass.Method,
		})
	}

	workspaceID, ok := s.workspaceScopeForList(w, r)
	if !ok {
		return
	}
	asyncJob, asyncJobErr := s.createAsyncCommandJob(id, workspaceID, cmd)
	if asyncJob != nil {
		w.Header().Set("X-Legator-Job-ID", asyncJob.ID)
	}
	if asyncJobErr != nil && jobs.IsAsyncQueueSaturated(asyncJobErr) {
		writeJSONError(w, http.StatusTooManyRequests, "queue_saturated", asyncJobErr.Error())
		return
	}

	switch decision.Outcome {
	case coreapprovalpolicy.CommandPolicyDecisionDeny:
		s.failAsyncJobByRequestID(cmd.RequestID, fmt.Sprintf("command denied by policy: %s", decision.ReasonCode), "", nil)
		s.emitAudit(audit.EventAuthorizationDenied, id, "api", fmt.Sprintf("Command denied by policy: %s (%s)", cmd.Command, decision.ReasonCode))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":           "denied",
			"policy_decision":  decision.Outcome,
			"gate_outcome":     decision.GateOutcome,
			"lane":             decision.Lane,
			"risk_level":       decision.RiskLevel,
			"risk_tier":        decision.RiskTier,
			"reason_code":      decision.ReasonCode,
			"policy_rationale": decision.Rationale,
			"message":          "Command denied by policy.",
		})
		return
	case coreapprovalpolicy.CommandPolicyDecisionQueue:
		approvalReason := "Manual command dispatch"
		if breakglass.Confirmed {
			approvalReason = fmt.Sprintf("%s (breakglass)", approvalReason)
		}
		requireSecondApprover := s.cfg.Approval.TwoPersonMode && decision.RiskTier >= 3 && decision.Policy.RequireSecondApprover
		req, err := s.approvalQueue.SubmitWithWorkspaceAndOptions(
			s.workspaceJobFilter(r),
			id,
			&cmd,
			approvalReason,
			decision.RiskLevel,
			"api",
			string(decision.Outcome),
			decision.Rationale,
			approval.SubmissionOptions{RequireSecondApprover: requireSecondApprover},
		)
		if err != nil {
			s.failAsyncJobByRequestID(cmd.RequestID, fmt.Sprintf("approval queue: %s", err.Error()), "", nil)
			writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", fmt.Sprintf("approval queue: %s", err.Error()))
			return
		}

		if asyncJob != nil {
			s.markAsyncJobWaitingApproval(asyncJob.ID, req.ID, &req.ExpiresAt, "command waiting for approval")
		}
		s.appendCommandStreamMarker(cmd.RequestID, cmdtracker.StreamEventApproval, "pending_approval", map[string]any{
			"approval_id": req.ID,
			"risk_level":  req.RiskLevel,
			"expires_at":  req.ExpiresAt,
			"lane":        decision.Lane,
		})
		s.emitAudit(audit.EventApprovalRequest, id, "api",
			fmt.Sprintf("Approval required for: %s (risk: %s, lane: %s)", cmd.Command, req.RiskLevel, decision.Lane))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":           "pending_approval",
			"approval_id":      req.ID,
			"risk_level":       req.RiskLevel,
			"risk_tier":        decision.RiskTier,
			"lane":             decision.Lane,
			"gate_outcome":     decision.GateOutcome,
			"reason_code":      decision.ReasonCode,
			"expires_at":       req.ExpiresAt,
			"policy_decision":  decision.Outcome,
			"policy_rationale": decision.Rationale,
			"message":          "Command requires human approval. Use POST /api/v1/approvals/{id}/decide to approve or deny.",
		})
		return
	}

	if asyncJob != nil && !wantWait && s.asyncJobsScheduler != nil {
		dispatchResult, dispatchErr := s.asyncJobsScheduler.DispatchNow(asyncJob.ID)
		if dispatchErr != nil {
			s.failAsyncJobByRequestID(cmd.RequestID, dispatchErr.Error(), "", nil)
			writeJSONError(w, http.StatusBadGateway, "bad_gateway", dispatchErr.Error())
			return
		}
		if dispatchResult.Outcome == jobs.AsyncDispatchOutcomeQueued {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "queued",
				"request_id": cmd.RequestID,
				"job_id":     asyncJob.ID,
				"reason":     dispatchResult.Reason,
			})
			return
		}
		renderDispatchCommandHTTP(w, &corecommanddispatch.CommandInvokeProjection{
			Surface:       corecommanddispatch.ProjectionDispatchSurfaceHTTP,
			RequestID:     cmd.RequestID,
			WaitForResult: false,
			Envelope: &corecommanddispatch.CommandResultEnvelope{
				RequestID:  cmd.RequestID,
				State:      corecommanddispatch.ResultStateDispatched,
				Dispatched: true,
			},
		})
		return
	}

	if asyncJob != nil {
		s.markAsyncJobRunning(asyncJob.ID)
	}

	if strings.EqualFold(ps.Type, fleet.ProbeTypeRemote) {
		projection := s.invokeRemoteCommand(r.Context(), ps, cmd, wantWait, wantStream)
		if projection != nil && projection.Envelope != nil && projection.Envelope.Dispatched {
			s.emitAudit(audit.EventCommandSent, id, "api", fmt.Sprintf("Command dispatched (remote): %s", cmd.Command))
			s.publishEvent(events.CommandDispatched, id, fmt.Sprintf("Remote command dispatched: %s", cmd.Command),
				map[string]string{"request_id": projection.RequestID, "command": cmd.Command})
			s.appendCommandStreamMarker(cmd.RequestID, cmdtracker.StreamEventDispatch, "command_dispatched", map[string]any{
				"probe_id": id,
				"command":  cmd.Command,
			})
		} else {
			message := "remote command dispatch failed"
			if projection != nil && projection.Envelope != nil && projection.Envelope.Err != nil {
				message = projection.Envelope.Err.Error()
			}
			s.failAsyncJobByRequestID(cmd.RequestID, message, "", nil)
		}
		renderDispatchCommandHTTP(w, projection)
		return
	}

	projection := corecommanddispatch.InvokeCommandForSurface(r.Context(), invokeInput, s.dispatchCore)
	if projection != nil && projection.Envelope != nil && projection.Envelope.Dispatched {
		s.emitAudit(audit.EventCommandSent, id, "api", fmt.Sprintf("Command dispatched: %s", cmd.Command))
		s.publishEvent(events.CommandDispatched, id, fmt.Sprintf("Command dispatched: %s", cmd.Command),
			map[string]string{"request_id": projection.RequestID, "command": cmd.Command})
		s.appendCommandStreamMarker(cmd.RequestID, cmdtracker.StreamEventDispatch, "command_dispatched", map[string]any{
			"probe_id": id,
			"command":  cmd.Command,
		})
	} else {
		message := "command dispatch failed"
		if projection != nil && projection.Envelope != nil && projection.Envelope.Err != nil {
			message = projection.Envelope.Err.Error()
		}
		s.failAsyncJobByRequestID(cmd.RequestID, message, "", nil)
	}

	if wantWait && projection != nil && projection.Envelope != nil {
		if projection.Envelope.Result != nil {
			result := projection.Envelope.Result
			output := strings.TrimSpace(result.Stdout)
			if output == "" {
				output = strings.TrimSpace(result.Stderr)
			}
			s.completeAsyncJobByRequestID(result.RequestID, result.ExitCode, output)
		} else if projection.Envelope.Err != nil {
			s.failAsyncJobByRequestID(cmd.RequestID, projection.Envelope.Err.Error(), "", nil)
		}
	}

	renderDispatchCommandHTTP(w, projection)
}

func (s *Server) handleSimulateCommandPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	var body struct {
		RequestID string                   `json:"request_id"`
		Command   string                   `json:"command"`
		Args      []string                 `json:"args"`
		Level     protocol.CapabilityLevel `json:"level"`
		PolicyID  string                   `json:"policy_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	body.Command = strings.TrimSpace(body.Command)
	if body.Command == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "command is required")
		return
	}

	cmd := protocol.CommandPayload{
		RequestID: body.RequestID,
		Command:   body.Command,
		Args:      append([]string(nil), body.Args...),
		Level:     body.Level,
	}

	override := (*coreapprovalpolicy.CommandPolicyProfile)(nil)
	if strings.TrimSpace(body.PolicyID) != "" {
		tpl, found := s.policyStore.Get(strings.TrimSpace(body.PolicyID))
		if !found {
			writeJSONError(w, http.StatusNotFound, "not_found", "policy template not found")
			return
		}
		override = &coreapprovalpolicy.CommandPolicyProfile{
			PolicyID:               tpl.ID,
			ExecutionClassRequired: tpl.ExecutionClassRequired,
			SandboxRequired:        tpl.SandboxRequired,
			ApprovalMode:           tpl.ApprovalMode,
			Breakglass:             tpl.Breakglass,
		}
	}

	decision := s.approvalCore.EvaluateCommandPolicyPreview(r.Context(), id, &cmd, ps.PolicyLevel, override)

	type previewCommand struct {
		RequestID string                   `json:"request_id,omitempty"`
		Command   string                   `json:"command"`
		Args      []string                 `json:"args,omitempty"`
		Level     protocol.CapabilityLevel `json:"level,omitempty"`
	}
	type previewResponse struct {
		ProbeID  string                                   `json:"probe_id"`
		Command  previewCommand                           `json:"command"`
		Decision coreapprovalpolicy.CommandPolicyDecision `json:"decision"`
	}

	resp := previewResponse{
		ProbeID: id,
		Command: previewCommand{
			RequestID: strings.TrimSpace(cmd.RequestID),
			Command:   cmd.Command,
			Args:      cmd.Args,
			Level:     cmd.Level,
		},
		Decision: decision,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) invokeRemoteCommand(ctx context.Context, ps *fleet.ProbeState, cmd protocol.CommandPayload, waitForResult, stream bool) *corecommanddispatch.CommandInvokeProjection {
	projection := &corecommanddispatch.CommandInvokeProjection{
		Surface:       corecommanddispatch.ProjectionDispatchSurfaceHTTP,
		RequestID:     cmd.RequestID,
		WaitForResult: waitForResult,
		Envelope: &corecommanddispatch.CommandResultEnvelope{
			RequestID:  cmd.RequestID,
			Dispatched: true,
		},
	}
	if s.remoteExecutor == nil {
		projection.Envelope.Dispatched = false
		projection.Envelope.Err = fmt.Errorf("remote executor unavailable")
		return projection
	}

	run := func(execCtx context.Context) *corecommanddispatch.CommandResultEnvelope {
		result, err := s.remoteExecutor.Execute(execCtx, ps, cmd, func(chunk protocol.OutputChunkPayload) {
			s.recordCommandOutputChunk(chunk, stream)
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				err = corecommanddispatch.ErrTimeout
			}
			s.failAsyncJobByRequestID(cmd.RequestID, err.Error(), "", nil)
			return &corecommanddispatch.CommandResultEnvelope{
				RequestID:  cmd.RequestID,
				Dispatched: true,
				Err:        err,
			}
		}

		s.recordAudit(audit.Event{
			Type:    audit.EventCommandResult,
			ProbeID: ps.ID,
			Actor:   "remote-probe",
			Summary: "Remote command completed: " + cmd.RequestID,
			Detail:  map[string]any{"exit_code": result.ExitCode, "duration_ms": result.Duration},
		})
		evtType := events.CommandCompleted
		if result.ExitCode != 0 {
			evtType = events.CommandFailed
		}
		s.publishEvent(evtType, ps.ID, fmt.Sprintf("Command %s exit=%d", cmd.RequestID, result.ExitCode),
			map[string]any{"request_id": cmd.RequestID, "exit_code": result.ExitCode})
		s.appendCommandStreamMarker(cmd.RequestID, cmdtracker.StreamEventResult, "command_result", map[string]any{
			"probe_id":    ps.ID,
			"exit_code":   result.ExitCode,
			"duration_ms": result.Duration,
			"truncated":   result.Truncated,
		})

		output := strings.TrimSpace(result.Stdout)
		if output == "" {
			output = strings.TrimSpace(result.Stderr)
		}
		s.completeAsyncJobByRequestID(cmd.RequestID, result.ExitCode, output)

		return &corecommanddispatch.CommandResultEnvelope{
			RequestID:  cmd.RequestID,
			Dispatched: true,
			Result:     result,
		}
	}

	if waitForResult {
		projection.Envelope = run(ctx)
		return projection
	}

	go func() {
		_ = run(context.Background())
	}()
	return projection
}

func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	newKey, err := api.GenerateAPIKey()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to generate api key")
		return
	}

	previousKey := ps.APIKey
	if err := s.fleetMgr.SetAPIKey(id, newKey); err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	rotation := protocol.KeyRotationPayload{
		NewKey: newKey,
	}
	if err := s.hub.SendTo(id, protocol.MsgKeyRotation, rotation); err != nil {
		_ = s.fleetMgr.SetAPIKey(id, previousKey)
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", err.Error())
		return
	}

	s.emitAudit(audit.EventProbeKeyRotated, id, "api", "Probe API key rotated")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   "rotated",
		"probe_id": id,
		"new_key":  newKey,
	})
}

func (s *Server) handleProbeUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	id := r.PathValue("id")
	_, ok := s.fleetMgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	var upd protocol.UpdatePayload
	if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if upd.URL == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "url is required")
		return
	}

	if err := s.hub.SendTo(id, protocol.MsgUpdate, upd); err != nil {
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", err.Error())
		return
	}

	s.emitAudit(audit.EventCommandSent, id, "api",
		fmt.Sprintf("Update dispatched: %s → %s", upd.Version, upd.URL))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "dispatched",
		"version": upd.Version,
	})
}

func (s *Server) handleSetTags(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	id := r.PathValue("id")
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if err := s.fleetMgr.SetTags(id, body.Tags); err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.emitAudit(audit.EventPolicyChanged, id, "api", fmt.Sprintf("Tags set: %v", body.Tags))
	ps, _ := s.fleetMgr.Get(id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"probe_id": id, "tags": ps.Tags})
}

func (s *Server) handleApplyPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	probeID := r.PathValue("id")
	policyID := r.PathValue("policyId")

	result, err := s.approvalCore.ApplyPolicyTemplate(probeID, policyID, func(targetProbeID string, pol *protocol.PolicyUpdatePayload) error {
		return s.hub.SendTo(targetProbeID, protocol.MsgPolicyUpdate, pol)
	})
	if err != nil {
		switch {
		case errors.Is(err, coreapprovalpolicy.ErrProbeNotFound):
			writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		case errors.Is(err, coreapprovalpolicy.ErrPolicyTemplateNotFound):
			writeJSONError(w, http.StatusNotFound, "not_found", "policy template not found")
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}
	if !result.Pushed {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "applied_locally",
			"note":   "probe offline, policy saved but not pushed",
		})
		return
	}

	s.emitAudit(audit.EventPolicyChanged, probeID, "api",
		fmt.Sprintf("Policy %s (%s) applied", result.Template.Name, result.Template.ID))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":    "applied",
		"probe_id":  probeID,
		"policy_id": policyID,
		"level":     string(result.Template.Level),
	})
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	if s.taskRunner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "no active LLM provider configured. Set LEGATOR_LLM_* env vars or activate a model profile in Model Dock")
		return
	}
	if s.taskRunner == s.managedTaskRunner && s.modelProviderMgr != nil && !s.modelProviderMgr.HasActiveProvider() {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "no active LLM provider configured. Set LEGATOR_LLM_* env vars or activate a model profile in Model Dock")
		return
	}

	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}

	var req struct {
		Task string `json:"task"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Task == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "task is required")
		return
	}

	s.logger.Info("task submitted", zap.String("probe", id), zap.String("task", req.Task))
	s.emitAudit(audit.EventCommandSent, id, "llm-task", fmt.Sprintf("Task submitted: %s", req.Task))

	result, err := s.taskRunner.Run(r.Context(), id, req.Task, ps.Inventory, ps.PolicyLevel)
	if err != nil {
		s.logger.Warn("task execution error", zap.String("probe", id), zap.Error(err))
		if errors.Is(err, modeldock.ErrNoActiveProvider) {
			writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "no active LLM provider configured. Set LEGATOR_LLM_* env vars or activate a model profile in Model Dock")
			return
		}
		writeJSONError(w, http.StatusBadGateway, "llm_unavailable", "LLM provider is unavailable: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) handleFleetInventory(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}

	inv := buildInventoryFromProbes(s.probesForRequest(r), fleet.InventoryFilter{
		Tag:    r.URL.Query().Get("tag"),
		Status: r.URL.Query().Get("status"),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(inv)
}

func buildInventoryFromProbes(probes []*fleet.ProbeState, filter fleet.InventoryFilter) fleet.FleetInventory {
	statusFilter := strings.ToLower(strings.TrimSpace(filter.Status))
	tagFilter := strings.ToLower(strings.TrimSpace(filter.Tag))

	result := fleet.FleetInventory{
		Probes: make([]fleet.ProbeInventorySummary, 0, len(probes)),
		Aggregates: fleet.FleetAggregates{
			ProbesByOS:      map[string]int{},
			TagDistribution: map[string]int{},
		},
	}

	for _, ps := range probes {
		if statusFilter != "" && strings.ToLower(ps.Status) != statusFilter {
			continue
		}
		if tagFilter != "" {
			ok := false
			for _, tag := range ps.Tags {
				if strings.EqualFold(tag, tagFilter) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}

		summary := fleet.ProbeInventorySummary{
			ID:          ps.ID,
			Hostname:    ps.Hostname,
			Status:      ps.Status,
			OS:          ps.OS,
			Arch:        ps.Arch,
			PolicyLevel: ps.PolicyLevel,
			Tags:        append([]string(nil), ps.Tags...),
			LastSeen:    ps.LastSeen,
		}
		if ps.Inventory != nil {
			if summary.Hostname == "" {
				summary.Hostname = ps.Inventory.Hostname
			}
			if ps.Inventory.OS != "" {
				summary.OS = ps.Inventory.OS
			}
			if ps.Inventory.Arch != "" {
				summary.Arch = ps.Inventory.Arch
			}
			summary.Kernel = ps.Inventory.Kernel
			summary.CollectedAt = ps.Inventory.CollectedAt
			summary.CPUs = ps.Inventory.CPUs
			summary.RAMBytes = ps.Inventory.MemTotal
			summary.DiskBytes = ps.Inventory.DiskTotal
		}
		result.Probes = append(result.Probes, summary)

		result.Aggregates.TotalProbes++
		if strings.EqualFold(summary.Status, "online") {
			result.Aggregates.Online++
		}
		result.Aggregates.TotalCPUs += summary.CPUs
		result.Aggregates.TotalRAMBytes += summary.RAMBytes
		osKey := strings.ToLower(strings.TrimSpace(summary.OS))
		if osKey == "" {
			osKey = "unknown"
		}
		result.Aggregates.ProbesByOS[osKey]++
		for _, tag := range summary.Tags {
			result.Aggregates.TagDistribution[tag]++
		}
	}

	sort.Slice(result.Probes, func(i, j int) bool {
		lhs := strings.ToLower(strings.TrimSpace(result.Probes[i].Hostname))
		rhs := strings.ToLower(strings.TrimSpace(result.Probes[j].Hostname))
		if lhs == "" {
			lhs = result.Probes[i].ID
		}
		if rhs == "" {
			rhs = result.Probes[j].ID
		}
		return lhs < rhs
	})

	return result
}

func (s *Server) handleFederationInventory(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.federationStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "federation read model unavailable")
		return
	}

	requested := federationFilterFromRequest(r)
	access := auth.FederationAccessScopeFromContext(r.Context())
	effective, authzErr := applyFederationAccessFilter(requested, access)
	if authzErr != nil {
		s.recordFederationAuthorizationDenied(r, auth.PermFleetRead, requested, effective, access, authzErr)
		writeJSONError(w, http.StatusForbidden, "forbidden_scope", authzErr.Error())
		return
	}

	inv := s.federationStore.Inventory(r.Context(), effective)
	s.recordFederationReadAudit(r, "api:federation_inventory", requested, effective, access, len(inv.Sources), len(inv.Probes))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(inv)
}

func (s *Server) handleFederationSummary(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.federationStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "federation read model unavailable")
		return
	}

	requested := federationFilterFromRequest(r)
	access := auth.FederationAccessScopeFromContext(r.Context())
	effective, authzErr := applyFederationAccessFilter(requested, access)
	if authzErr != nil {
		s.recordFederationAuthorizationDenied(r, auth.PermFleetRead, requested, effective, access, authzErr)
		writeJSONError(w, http.StatusForbidden, "forbidden_scope", authzErr.Error())
		return
	}

	summary := s.federationStore.Summary(r.Context(), effective)
	s.recordFederationReadAudit(r, "api:federation_summary", requested, effective, access, len(summary.Sources), summary.Aggregates.TotalProbes)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}

func federationFilterFromRequest(r *http.Request) fleet.FederationFilter {
	q := r.URL.Query()
	return fleet.FederationFilter{
		Tag:      q.Get("tag"),
		Status:   q.Get("status"),
		Source:   q.Get("source"),
		Cluster:  q.Get("cluster"),
		Site:     q.Get("site"),
		Search:   q.Get("search"),
		TenantID: firstNonEmptyFederationQueryParam(q.Get("tenant_id"), q.Get("tenant")),
		OrgID:    firstNonEmptyFederationQueryParam(q.Get("org_id"), q.Get("org")),
		ScopeID:  firstNonEmptyFederationQueryParam(q.Get("scope_id"), q.Get("scope")),
	}
}

func (s *Server) handleFleetSummary(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	scorecard := s.buildReliabilityScorecard(reliabilityDefaultWindow)
	counts := map[string]int{}
	for _, ps := range s.probesForRequest(r) {
		counts[ps.Status]++
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"counts":            counts,
		"connected":         counts["online"],
		"pending_approvals": s.approvalQueue.PendingCount(),
		"reliability":       scorecard,
	})
}

func (s *Server) handleFleetTags(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	tags := map[string]int{}
	for _, ps := range s.probesForRequest(r) {
		for _, t := range ps.Tags {
			tags[t]++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tags": tags})
}

func (s *Server) handleListByTag(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	tag := r.PathValue("tag")
	all := s.fleetMgr.ListByTag(tag)
	// Filter by tenant scope.
	scoped := s.probesForRequest(r)
	scopedSet := make(map[string]bool, len(scoped))
	for _, ps := range scoped {
		scopedSet[ps.ID] = true
	}
	out := make([]*fleet.ProbeState, 0, len(all))
	for _, ps := range all {
		if scopedSet[ps.ID] {
			out = append(out, ps)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleGroupCommand(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	tag := r.PathValue("tag")
	byTag := s.fleetMgr.ListByTag(tag)
	// Apply tenant scope.
	scopedSet := make(map[string]bool, len(s.probesForRequest(r)))
	for _, ps := range s.probesForRequest(r) {
		scopedSet[ps.ID] = true
	}
	probes := make([]*fleet.ProbeState, 0, len(byTag))
	for _, ps := range byTag {
		if scopedSet[ps.ID] {
			probes = append(probes, ps)
		}
	}
	if len(probes) == 0 {
		writeJSONError(w, http.StatusNotFound, "not_found", "no probes with that tag")
		return
	}

	var cmd protocol.CommandPayload
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	results := make([]map[string]string, 0, len(probes))
	for _, ps := range probes {
		rid := fmt.Sprintf("grp-%s-%d", ps.ID[:8], time.Now().UnixNano()%100000)
		c := cmd
		c.RequestID = rid
		if err := s.hub.SendTo(ps.ID, protocol.MsgCommand, c); err != nil {
			results = append(results, map[string]string{
				"probe_id": ps.ID, "status": "error", "error": err.Error(),
			})
		} else {
			results = append(results, map[string]string{
				"probe_id": ps.ID, "status": "dispatched", "request_id": rid,
			})
		}
	}

	s.emitAudit(audit.EventCommandSent, tag, "api",
		fmt.Sprintf("Group command to %d probes (tag=%s): %s", len(probes), tag, cmd.Command))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tag":     tag,
		"total":   len(probes),
		"results": results,
	})
}

// ── Approvals ────────────────────────────────────────────────

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermApprovalRead) {
		return
	}
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}

	status := r.URL.Query().Get("status")
	w.Header().Set("Content-Type", "application/json")

	wsID := s.workspaceJobFilter(r)
	if status == "pending" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"approvals":     s.approvalQueue.PendingByWorkspace(wsID),
			"pending_count": s.approvalQueue.PendingCount(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"approvals":     s.approvalQueue.AllByWorkspace(wsID, limit),
		"pending_count": s.approvalQueue.PendingCount(),
	})
}

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermApprovalRead) {
		return
	}
	id := r.PathValue("id")
	wsID := s.workspaceJobFilter(r)
	req, ok := s.approvalQueue.GetCheckWorkspace(id, wsID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "approval request not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(req)
}

func (s *Server) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermApprovalWrite) {
		return
	}
	id := r.PathValue("id")
	wsID := s.workspaceJobFilter(r)
	if wsID != "" {
		if _, ok := s.approvalQueue.GetCheckWorkspace(id, wsID); !ok {
			writeJSONError(w, http.StatusNotFound, "not_found", "approval request not found")
			return
		}
	}

	projection := orchestrateDecideApprovalHTTP(r.Body, func(body *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		return s.approvalCore.DecideAndDispatch(id, body.Decision, body.DecidedBy, s.dispatchApprovedCommand)
	})
	renderDecideApprovalHTTP(w, projection)
}

// ── Audit ────────────────────────────────────────────────────

func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAuditRead) {
		return
	}

	filter, err := auditFilterFromRequest(r, false)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if wsID := s.workspaceJobFilter(r); wsID != "" {
		filter.WorkspaceID = wsID
	}

	total := s.countAudit()
	if filter.Cursor != "" && s.auditStore != nil {
		pageFilter := filter
		pageFilter.Limit = filter.Limit + 1 // sentinel row for has_more
		events, err := s.auditStore.QueryPersisted(pageFilter)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}

		hasMore := len(events) > filter.Limit
		if hasMore {
			events = events[:filter.Limit]
		}

		nextCursor := ""
		if hasMore && len(events) > 0 {
			nextCursor = events[len(events)-1].ID
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events":      events,
			"total":       total,
			"next_cursor": nextCursor,
			"has_more":    hasMore,
		})
		return
	}

	events := s.queryAudit(filter)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"events":      events,
		"total":       total,
		"next_cursor": "",
		"has_more":    false,
	})
}

func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAuditRead) {
		return
	}
	if s.auditStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "audit verify requires persistent audit store")
		return
	}

	result, err := s.auditStore.VerifyChain(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	firstInvalidAt := any(nil)
	if result.FirstInvalidAt != nil {
		firstInvalidAt = *result.FirstInvalidAt
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"valid":            result.Valid,
		"entries_checked":  result.EntriesChecked,
		"first_invalid_at": firstInvalidAt,
	})
}

func (s *Server) handleAuditExportJSONL(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAuditRead) {
		return
	}
	if s.auditStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "audit export requires persistent audit store")
		return
	}

	filter, err := auditFilterFromRequest(r, true)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if wsID := s.workspaceJobFilter(r); wsID != "" {
		filter.WorkspaceID = wsID
	}

	filename := fmt.Sprintf("legator-audit-%s.jsonl", time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if s.auditStore.ChainModeEnabled() {
		w.Header().Set("X-Legator-Audit-Chain-Mode", "enabled")
		w.Header().Set("X-Legator-Audit-Chain-Algorithm", audit.ChainAlgorithm())
		w.Header().Set("X-Legator-Audit-Genesis-Hash", audit.GenesisHash)
	}

	if err := s.auditStore.StreamJSONL(r.Context(), w, filter); err != nil {
		s.logger.Warn("stream audit jsonl export failed", zap.Error(err))
	}
}

func (s *Server) handleAuditExportCSV(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAuditRead) {
		return
	}
	if s.auditStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "audit export requires persistent audit store")
		return
	}

	filter, err := auditFilterFromRequest(r, true)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if wsID := s.workspaceJobFilter(r); wsID != "" {
		filter.WorkspaceID = wsID
	}

	filename := fmt.Sprintf("legator-audit-%s.csv", time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if s.auditStore.ChainModeEnabled() {
		w.Header().Set("X-Legator-Audit-Chain-Mode", "enabled")
		w.Header().Set("X-Legator-Audit-Chain-Algorithm", audit.ChainAlgorithm())
		w.Header().Set("X-Legator-Audit-Genesis-Hash", audit.GenesisHash)
	}

	if err := s.auditStore.StreamCSV(r.Context(), w, filter); err != nil {
		s.logger.Warn("stream audit csv export failed", zap.Error(err))
	}
}

func (s *Server) handleAuditPurge(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAdmin) {
		return
	}
	if s.auditStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "audit purge requires persistent audit store")
		return
	}

	raw := strings.TrimSpace(r.URL.Query().Get("older_than"))
	if raw == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "older_than is required")
		return
	}

	olderThan, err := parseHumanDuration(raw)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid older_than duration")
		return
	}

	deleted, err := s.auditStore.Purge(olderThan)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
}

func auditFilterFromRequest(r *http.Request, allowUntil bool) (audit.Filter, error) {
	filter := audit.Filter{ProbeID: strings.TrimSpace(r.URL.Query().Get("probe_id")), Limit: 50}

	if rawType := strings.TrimSpace(r.URL.Query().Get("type")); rawType != "" {
		filter.Type = audit.EventType(rawType)
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit <= 0 {
			return filter, fmt.Errorf("invalid limit")
		}
		filter.Limit = limit
	}
	if rawSince := strings.TrimSpace(r.URL.Query().Get("since")); rawSince != "" {
		since, err := parseRFC3339(rawSince)
		if err != nil {
			return filter, fmt.Errorf("invalid since timestamp")
		}
		filter.Since = since
	}
	if allowUntil {
		if rawUntil := strings.TrimSpace(r.URL.Query().Get("until")); rawUntil != "" {
			until, err := parseRFC3339(rawUntil)
			if err != nil {
				return filter, fmt.Errorf("invalid until timestamp")
			}
			filter.Until = until
		}
	}
	filter.Cursor = strings.TrimSpace(r.URL.Query().Get("cursor"))

	return filter, nil
}

func parseRFC3339(raw string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC(), nil
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

// ── Commands ─────────────────────────────────────────────────

func (s *Server) handlePendingCommands(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"pending":   s.cmdTracker.ListPending(),
		"in_flight": s.cmdTracker.InFlight(),
	})
}

func (s *Server) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	requestID := strings.TrimSpace(r.PathValue("requestId"))
	if requestID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "request_id required")
		return
	}

	// Workspace isolation: resolve the async job owning this request and check workspace.
	if wsID := s.workspaceJobFilter(r); wsID != "" && s.jobsStore != nil {
		if existing, wsErr := s.jobsStore.GetAsyncJobByRequestID(requestID); wsErr == nil {
			if existing.WorkspaceID != "" && existing.WorkspaceID != wsID {
				writeJSONError(w, http.StatusForbidden, "workspace_forbidden", "access to this stream is not permitted for your workspace")
				return
			}
		}
	}

	query, err := commandReplayQueryFromRequest(r, requestID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.commandStreams == nil {
		sub, cleanup := s.hub.SubscribeStream(requestID, 256)
		defer cleanup()
		for {
			select {
			case <-r.Context().Done():
				return
			case chunk := <-sub.Ch:
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				if chunk.Final {
					return
				}
			}
		}
	}

	replay, sub, cleanup, err := s.commandStreams.ReplayAndSubscribe(requestID, query, 256)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer cleanup()

	meta, _ := json.Marshal(map[string]any{
		"request_id":      requestID,
		"earliest_seq":    replay.EarliestSeq,
		"latest_seq":      replay.LatestSeq,
		"next_seq":        replay.NextSeq,
		"resume_token":    replay.ResumeToken,
		"has_more":        replay.HasMore,
		"truncated":       replay.Truncated,
		"missed_from_seq": replay.MissedFromSeq,
		"missed_to_seq":   replay.MissedToSeq,
	})
	fmt.Fprintf(w, "event: replay.meta\ndata: %s\n\n", meta)
	flusher.Flush()

	for _, evt := range replay.Events {
		if !writeCommandStreamSSEEvent(w, flusher, evt) {
			return
		}
		if evt.Kind == cmdtracker.StreamEventOutput && evt.Final {
			return
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case evt := <-sub.Ch:
			if !writeCommandStreamSSEEvent(w, flusher, evt) {
				return
			}
			if evt.Kind == cmdtracker.StreamEventOutput && evt.Final {
				return
			}
		}
	}
}

func (s *Server) handleCommandReplay(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	requestID := strings.TrimSpace(r.PathValue("requestId"))
	if requestID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "request_id required")
		return
	}

	// Workspace isolation: resolve the async job owning this request and check workspace.
	if wsID := s.workspaceJobFilter(r); wsID != "" && s.jobsStore != nil {
		if existing, wsErr := s.jobsStore.GetAsyncJobByRequestID(requestID); wsErr == nil {
			if existing.WorkspaceID != "" && existing.WorkspaceID != wsID {
				writeJSONError(w, http.StatusForbidden, "workspace_forbidden", "access to this stream is not permitted for your workspace")
				return
			}
		}
	}

	if s.commandStreams == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "command stream replay unavailable")
		return
	}

	query, err := commandReplayQueryFromRequest(r, requestID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	replay, err := s.commandStreams.Replay(requestID, query)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"request_id": requestID,
		"replay":     replay,
	})
}

func commandReplayQueryFromRequest(r *http.Request, requestID string) (cmdtracker.StreamReplayQuery, error) {
	query := cmdtracker.StreamReplayQuery{}
	if r == nil || r.URL == nil {
		return query, nil
	}
	if rawToken := strings.TrimSpace(r.URL.Query().Get("resume_token")); rawToken != "" {
		query.ResumeToken = rawToken
	}
	if rawToken := strings.TrimSpace(r.URL.Query().Get("cursor")); rawToken != "" && query.ResumeToken == "" {
		query.ResumeToken = rawToken
	}
	if rawSeq := strings.TrimSpace(r.URL.Query().Get("last_seq")); rawSeq != "" {
		seq, err := strconv.ParseInt(rawSeq, 10, 64)
		if err != nil || seq < 0 {
			return cmdtracker.StreamReplayQuery{}, fmt.Errorf("last_seq must be a non-negative integer")
		}
		query.LastSeq = seq
	}
	if rawSeq := strings.TrimSpace(r.Header.Get("Last-Event-ID")); rawSeq != "" {
		seq, err := strconv.ParseInt(rawSeq, 10, 64)
		if err == nil && seq > query.LastSeq {
			query.LastSeq = seq
		}
	}
	if rawSince := strings.TrimSpace(r.URL.Query().Get("since")); rawSince != "" {
		since, err := parseRFC3339(rawSince)
		if err != nil {
			return cmdtracker.StreamReplayQuery{}, fmt.Errorf("invalid since timestamp")
		}
		query.Since = &since
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit <= 0 {
			return cmdtracker.StreamReplayQuery{}, fmt.Errorf("limit must be a positive integer")
		}
		query.Limit = limit
	} else {
		query.Limit = 2000
	}
	if _, _, err := cmdtracker.DecodeResumeToken(query.ResumeToken); query.ResumeToken != "" && err != nil {
		return cmdtracker.StreamReplayQuery{}, err
	}
	if query.ResumeToken != "" {
		tokenReqID, _, _ := cmdtracker.DecodeResumeToken(query.ResumeToken)
		if tokenReqID != "" && tokenReqID != requestID {
			return cmdtracker.StreamReplayQuery{}, fmt.Errorf("resume token request_id mismatch")
		}
	}
	return query, nil
}

func writeCommandStreamSSEEvent(w http.ResponseWriter, flusher http.Flusher, evt cmdtracker.StreamEvent) bool {
	payload, err := json.Marshal(evt)
	if err != nil {
		return false
	}
	eventName := string(evt.Kind)
	if eventName == "" {
		eventName = "message"
	}
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", evt.Seq, eventName, payload)
	flusher.Flush()
	return true
}

// ── Events SSE ───────────────────────────────────────────────

func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	subID := fmt.Sprintf("sse-%d", time.Now().UnixNano())
	ch := s.eventBus.Subscribe(subID)
	defer s.eventBus.Unsubscribe(subID)

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, evt.JSON())
			flusher.Flush()
		}
	}
}

// ── Policy templates ─────────────────────────────────────────

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.policyStore.List())
}

func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")
	tpl, ok := s.policyStore.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "policy template not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tpl)
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	var body struct {
		Name        string                   `json:"name"`
		Description string                   `json:"description"`
		Level       protocol.CapabilityLevel `json:"level"`
		Allowed     []string                 `json:"allowed"`
		Blocked     []string                 `json:"blocked"`
		Paths       []string                 `json:"paths"`

		ExecutionClassRequired protocol.ExecutionClass   `json:"execution_class_required"`
		SandboxRequired        *bool                     `json:"sandbox_required"`
		ApprovalMode           protocol.ApprovalMode     `json:"approval_mode"`
		RequireSecondApprover  *bool                     `json:"require_second_approver"`
		Breakglass             protocol.BreakglassPolicy `json:"breakglass"`
		MaxRuntimeSec          int                       `json:"max_runtime_sec"`
		AllowedScopes          []string                  `json:"allowed_scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if body.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name required")
		return
	}

	opts := controlpolicy.DefaultTemplateOptionsForLevel(body.Level)
	if body.ExecutionClassRequired != "" {
		opts.ExecutionClassRequired = body.ExecutionClassRequired
	}
	if body.SandboxRequired != nil {
		opts.SandboxRequired = *body.SandboxRequired
	}
	if body.ApprovalMode != "" {
		opts.ApprovalMode = body.ApprovalMode
	}
	if body.RequireSecondApprover != nil {
		opts.RequireSecondApprover = *body.RequireSecondApprover
		opts.RequireSecondApproverSet = true
	}
	if body.Breakglass.Enabled || body.Breakglass.RequireTypedConfirmation || len(body.Breakglass.AllowedReasons) > 0 {
		opts.Breakglass = body.Breakglass
	}
	if body.MaxRuntimeSec != 0 {
		opts.MaxRuntimeSec = body.MaxRuntimeSec
	}
	if body.AllowedScopes != nil {
		opts.AllowedScopes = body.AllowedScopes
	}
	opts = controlpolicy.NormalizeTemplateOptions(opts)

	if err := controlpolicy.ValidateExecutionClass(opts.ExecutionClassRequired); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := controlpolicy.ValidateApprovalMode(opts.ApprovalMode); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if opts.MaxRuntimeSec < 0 || opts.MaxRuntimeSec > controlpolicy.MaxPolicyRuntimeSec {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("max_runtime_sec must be between 0 and %d", controlpolicy.MaxPolicyRuntimeSec))
		return
	}
	if err := controlpolicy.ValidateBreakglass(opts.Breakglass); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := controlpolicy.ValidateAllowedScopes(opts.AllowedScopes); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	tpl := s.policyStore.Create(body.Name, body.Description, body.Level, body.Allowed, body.Blocked, body.Paths, opts)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(tpl)
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	id := r.PathValue("id")
	if err := s.policyStore.Delete(id); err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Downloads ────────────────────────────────────────────────

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	releasesDir := filepath.Join(s.cfg.DataDir, "releases")
	filePath := filepath.Join(releasesDir, filepath.Base(filename))
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(filename)))
	http.ServeFile(w, r, filePath)
}

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	installScript := filepath.Join("install", "install.sh")
	if _, err := os.Stat(installScript); os.IsNotExist(err) {
		writeJSONError(w, http.StatusNotFound, "not_found", "install script not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeFile(w, r, installScript)
}

// ── Web UI pages ─────────────────────────────────────────────

func (s *Server) handleFleetPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Legator Control Plane</title></head>
<body>
<h1>Legator Control Plane</h1>
<p>Version: %s (%s)</p>
<p><a href="/api/v1/probes">Fleet API</a> | <a href="/api/v1/fleet/summary">Summary</a> | <a href="/api/v1/reliability/scorecard">Reliability</a> | <a href="/api/v1/approvals?status=pending">Approvals</a></p>
</body></html>`, Version, Commit)
		return
	}

	probes := s.fleetMgr.List()
	sort.Slice(probes, func(i, j int) bool {
		lhs := strings.ToLower(probes[i].Hostname)
		if lhs == "" {
			lhs = probes[i].ID
		}
		rhs := strings.ToLower(probes[j].Hostname)
		if rhs == "" {
			rhs = probes[j].ID
		}
		return lhs < rhs
	})

	counts := s.fleetMgr.Count()
	reliabilityScorecard := s.buildReliabilityScorecard(reliabilityDefaultWindow)
	data := FleetPageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "fleet",
		},
		Probes: probes,
		Summary: FleetSummary{
			Online:            counts["online"],
			Offline:           counts["offline"],
			Degraded:          counts["degraded"],
			Total:             len(probes),
			ReliabilityScore:  reliabilityScorecard.Overall.Score,
			ReliabilityStatus: reliabilityScorecard.Overall.Status,
		},
		Commit: Commit,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages.Render(w, "fleet", data); err != nil {
		s.logger.Error("failed to render fleet page", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func (s *Server) handleFederationPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Federation</h1><p>Template not loaded</p>")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "federation",
	}
	if err := s.pages.Render(w, "federation", data); err != nil {
		s.logger.Error("failed to render federation page", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func (s *Server) handleProbeDetailPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
	if !ok {
		ps = &fleet.ProbeState{
			ID:          id,
			Status:      "offline",
			PolicyLevel: protocol.CapObserve,
		}
	}

	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<h1>Probe: %s</h1><p>Status: %s</p>`, id, ps.Status)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := ProbePageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "fleet",
		},
		Probe:  ps,
		Uptime: calculateUptime(ps.Registered),
	}
	if err := s.pages.Render(w, "probe-detail", data); err != nil {
		s.logger.Error("failed to render probe detail", zap.String("probe", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func (s *Server) handleFleetGetMessages(w http.ResponseWriter, r *http.Request) {
	request := cloneFleetChatAPIRequest(r)
	if s.chatStore != nil {
		s.chatStore.HandleGetMessages(w, request)
		return
	}
	s.chatMgr.HandleGetMessages(w, request)
}

func (s *Server) handleFleetSendMessage(w http.ResponseWriter, r *http.Request) {
	request := cloneFleetChatAPIRequest(r)
	if s.chatStore != nil {
		s.chatStore.HandleSendMessage(w, request)
		return
	}
	s.chatMgr.HandleSendMessage(w, request)
}

func (s *Server) handleFleetChatWS(w http.ResponseWriter, r *http.Request) {
	request := r.Clone(r.Context())
	urlCopy := *r.URL
	q := urlCopy.Query()
	q.Set("probe_id", "fleet")
	urlCopy.RawQuery = q.Encode()
	request.URL = &urlCopy

	if s.chatStore != nil {
		s.chatStore.HandleChatWS(w, request)
		return
	}
	s.chatMgr.HandleChatWS(w, request)
}

func cloneFleetChatAPIRequest(r *http.Request) *http.Request {
	request := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = "/api/v1/probes/fleet/chat"
	request.URL = &urlCopy
	return request
}

func (s *Server) handleFleetChatPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}

	inv := s.fleetMgr.Inventory(fleet.InventoryFilter{})
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h1>Fleet Chat</h1><p>Template not loaded</p>`)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := FleetChatPageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "fleet-chat",
		},
		Inventory: inv,
	}
	if err := s.pages.Render(w, "fleet-chat", data); err != nil {
		s.logger.Error("failed to render fleet chat", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func (s *Server) handleChatPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
	if !ok {
		ps = &fleet.ProbeState{
			ID:          id,
			Status:      "offline",
			PolicyLevel: protocol.CapObserve,
		}
	}

	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<h1>Chat: %s</h1><p>Template not loaded</p>`, id)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := ProbePageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "fleet",
		},
		Probe:  ps,
		Uptime: calculateUptime(ps.Registered),
	}
	if err := s.pages.Render(w, "chat", data); err != nil {
		s.logger.Error("failed to render chat", zap.String("probe", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func (s *Server) handleApprovalsPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermApprovalRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Approval Queue</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "approvals",
	}
	if err := s.pages.Render(w, "approvals", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAuditRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Audit Log</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "audit",
	}
	if err := s.pages.Render(w, "audit", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleAlertsPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Alerts</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := AlertsPageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "alerts",
		},
	}
	if err := s.pages.Render(w, "alerts", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleModelDockPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Model Dock</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "model-dock",
	}
	if err := s.pages.Render(w, "model-dock", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleCloudConnectorsPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Cloud Connectors</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "cloud-connectors",
	}
	if err := s.pages.Render(w, "cloud-connectors", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleDiscoveryPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Discovery</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "discovery",
	}
	if err := s.pages.Render(w, "discovery", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleJobsPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Jobs</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "jobs",
	}
	if err := s.pages.Render(w, "jobs", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func (s *Server) handleDeleteProbe(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing probe id")
		return
	}

	// Disconnect if currently connected
	_ = s.hub.SendTo(id, protocol.MsgCommand, protocol.CommandPayload{
		RequestID: "disconnect",
		Command:   "__disconnect",
	})

	if err := s.fleetMgr.Delete(id); err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	s.emitAudit(audit.EventProbeDeregistered, id, "api", fmt.Sprintf("probe %s deleted", id))
	s.logger.Info("probe deleted", zap.String("id", id))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"deleted":"%s"}`, id)
}

func (s *Server) handleFleetCleanup(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	// Default: remove probes offline for more than 1 hour
	threshold := time.Hour
	if raw := r.URL.Query().Get("older_than"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			threshold = d
		}
	}

	removed := s.fleetMgr.CleanupOffline(threshold)

	for _, id := range removed {
		s.emitAudit(audit.EventProbeDeregistered, id, "cleanup", fmt.Sprintf("stale probe removed (offline > %s)", threshold))
	}

	s.logger.Info("fleet cleanup", zap.Int("removed", len(removed)), zap.Duration("threshold", threshold))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"removed": removed,
		"count":   len(removed),
	})
}

func (s *Server) handleAlertsUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "alerts engine unavailable")
}

func (s *Server) handleJobsUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "jobs scheduler unavailable")
}

func (s *Server) handleModelDockUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "model dock unavailable")
}

func (s *Server) handleCloudConnectorsUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "cloud connectors unavailable")
}

func (s *Server) handleAutomationPacksUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "automation packs unavailable")
}

func (s *Server) handleKubeflowUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "kubeflow adapter unavailable")
}

func (s *Server) handleGrafanaUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "grafana adapter unavailable")
}

func (s *Server) handleDiscoveryUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "discovery unavailable")
}

func (s *Server) handleComplianceUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "compliance engine unavailable")
}

func (s *Server) handleCompliancePage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Compliance</h1><p>Template not loaded</p>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "compliance",
	}
	if err := s.pages.Render(w, "compliance", data); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// handleOpenAPISpec serves the OpenAPI 3.1 specification from docs/openapi.yaml.
// No authentication is required — the spec is public API documentation.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	specPath := filepath.Join("docs", "openapi.yaml")
	data, err := os.ReadFile(specPath)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "OpenAPI spec not available")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ── Tenant API handlers ──────────────────────────────────────────────────────

// handleCreateTenant creates a new tenant (admin only).
//
// POST /api/v1/tenants
func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAdmin) {
		return
	}
	if s.tenantStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "multi-tenancy not enabled")
		return
	}
	var req struct {
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		ContactEmail string `json:"contact_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	t, err := s.tenantStore.Create(req.Name, req.Slug, req.ContactEmail)
	if err != nil {
		if errors.Is(err, tenant.ErrSlugConflict) {
			writeJSONError(w, http.StatusConflict, "slug_conflict", "tenant slug already exists")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(t)
}

// handleListTenants returns all tenants the current user can see.
//
// GET /api/v1/tenants
func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.tenantStore == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tenants": []any{}})
		return
	}
	all, err := s.tenantStore.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list tenants")
		return
	}
	// Filter by scope for non-admin users.
	scope := s.resolveTenantScope(r.Context())
	var visible []*tenant.Tenant
	if scope.IsAdmin {
		visible = all
	} else {
		for _, t := range all {
			if scope.AllowsTenant(t.ID) {
				visible = append(visible, t)
			}
		}
	}
	if visible == nil {
		visible = []*tenant.Tenant{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tenants": visible})
}

// handleGetTenant returns a single tenant by ID.
//
// GET /api/v1/tenants/{id}
func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.tenantStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "multi-tenancy not enabled")
		return
	}
	id := r.PathValue("id")
	t, err := s.tenantStore.Get(id)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "tenant not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get tenant")
		return
	}
	// Non-admin users may only view their own tenants.
	scope := s.resolveTenantScope(r.Context())
	if !scope.IsAdmin && !scope.AllowsTenant(t.ID) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "access denied")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(t)
}

// handleUpdateTenant updates a tenant's mutable fields (admin only).
//
// PATCH /api/v1/tenants/{id}
func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAdmin) {
		return
	}
	if s.tenantStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "multi-tenancy not enabled")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Name         string `json:"name"`
		ContactEmail string `json:"contact_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	updated, err := s.tenantStore.Update(id, req.Name, req.ContactEmail)
	if err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "tenant not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

// handleDeleteTenant deletes a tenant (admin only, only if no probes are assigned).
//
// DELETE /api/v1/tenants/{id}
func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAdmin) {
		return
	}
	if s.tenantStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "multi-tenancy not enabled")
		return
	}
	id := r.PathValue("id")

	// Guard: refuse if any probes are assigned to this tenant.
	for _, ps := range s.fleetMgr.ListByTenant(id) {
		if ps != nil {
			writeJSONError(w, http.StatusConflict, "has_probes", "tenant has active probes; re-assign or delete them first")
			return
		}
	}

	if err := s.tenantStore.Delete(id); err != nil {
		if errors.Is(err, tenant.ErrTenantNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "tenant not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to delete tenant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAssignUserTenants replaces the tenant memberships for a user (admin only).
//
// PUT /api/v1/users/{id}/tenants
func (s *Server) handleAssignUserTenants(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAdmin) {
		return
	}
	if s.tenantStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "multi-tenancy not enabled")
		return
	}
	userID := r.PathValue("id")
	var req struct {
		TenantIDs []string `json:"tenant_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if req.TenantIDs == nil {
		req.TenantIDs = []string{}
	}
	if err := s.tenantStore.SetUserTenants(userID, req.TenantIDs); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to assign tenants")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"user_id": userID, "tenant_ids": req.TenantIDs})
}
