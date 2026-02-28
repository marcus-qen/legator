package server

import (
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

	"github.com/marcus-qen/legator/internal/controlplane/api"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/metrics"
	"github.com/marcus-qen/legator/internal/controlplane/modeldock"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Health + version
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)

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
	mux.HandleFunc("GET /api/v1/probes", s.handleListProbes)
	mux.HandleFunc("GET /api/v1/probes/{id}", s.handleGetProbe)
	mux.HandleFunc("GET /api/v1/probes/{id}/health", s.handleProbeHealth)
	mux.HandleFunc("POST /api/v1/probes/{id}/command", s.handleDispatchCommand)
	mux.HandleFunc("POST /api/v1/probes/{id}/rotate-key", s.handleRotateKey)
	mux.HandleFunc("POST /api/v1/probes/{id}/update", s.handleProbeUpdate)
	mux.HandleFunc("PUT /api/v1/probes/{id}/tags", s.handleSetTags)
	mux.HandleFunc("POST /api/v1/probes/{id}/apply-policy/{policyId}", s.handleApplyPolicy)
	mux.HandleFunc("POST /api/v1/probes/{id}/task", s.handleTask)
	mux.HandleFunc("DELETE /api/v1/probes/{id}", s.handleDeleteProbe)
	mux.HandleFunc("GET /api/v1/fleet/summary", s.handleFleetSummary)
	mux.HandleFunc("GET /api/v1/fleet/inventory", s.handleFleetInventory)
	mux.HandleFunc("GET /api/v1/fleet/tags", s.handleFleetTags)
	mux.HandleFunc("GET /api/v1/fleet/by-tag/{tag}", s.handleListByTag)
	mux.HandleFunc("POST /api/v1/fleet/by-tag/{tag}/command", s.handleGroupCommand)
	mux.HandleFunc("POST /api/v1/fleet/cleanup", s.handleFleetCleanup)

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

	// Metrics
	metricsCollector := metrics.NewCollector(
		s.fleetMgr,
		&hubConnectedAdapter{hub: s.hub},
		s.approvalQueue,
		s.metricsAuditCounter(),
	)
	s.webhookNotifier.SetDeliveryObserver(metricsCollector)
	mux.HandleFunc("GET /api/v1/metrics", s.withPermission(auth.PermFleetRead, metricsCollector.Handler()))

	// Approvals
	mux.HandleFunc("GET /api/v1/approvals", s.handleListApprovals)
	mux.HandleFunc("GET /api/v1/approvals/{id}", s.handleGetApproval)
	mux.HandleFunc("POST /api/v1/approvals/{id}/decide", s.handleDecideApproval)

	// Audit
	mux.HandleFunc("GET /api/v1/audit", s.handleAuditLog)
	mux.HandleFunc("GET /api/v1/audit/export", s.handleAuditExportJSONL)
	mux.HandleFunc("GET /api/v1/audit/export/csv", s.handleAuditExportCSV)
	mux.HandleFunc("DELETE /api/v1/audit/purge", s.handleAuditPurge)

	// Events SSE stream
	mux.HandleFunc("GET /api/v1/events", s.handleEventsSSE)

	if s.mcpServer != nil {
		mux.Handle("GET /mcp", s.mcpServer.Handler())
		mux.Handle("POST /mcp", s.mcpServer.Handler())
	}

	// Commands
	mux.HandleFunc("GET /api/v1/commands/pending", s.handlePendingCommands)
	mux.HandleFunc("GET /api/v1/commands/{requestId}/stream", s.handleSSEStream)

	// Policy templates
	mux.HandleFunc("GET /api/v1/policies", s.handleListPolicies)
	mux.HandleFunc("GET /api/v1/policies/{id}", s.handleGetPolicy)
	mux.HandleFunc("POST /api/v1/policies", s.handleCreatePolicy)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", s.handleDeletePolicy)

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
		mux.HandleFunc("GET /api/v1/alerts/{id}", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleGetRule))
		mux.HandleFunc("PUT /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleUpdateRule))
		mux.HandleFunc("DELETE /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.alertEngine.HandleDeleteRule))
		mux.HandleFunc("GET /api/v1/alerts/{id}/history", s.withPermission(auth.PermFleetRead, s.alertEngine.HandleRuleHistory))
	} else {
		mux.HandleFunc("GET /api/v1/alerts", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("POST /api/v1/alerts", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/alerts/active", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/alerts/{id}", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
		mux.HandleFunc("PUT /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("DELETE /api/v1/alerts/{id}", s.withPermission(auth.PermFleetWrite, s.handleAlertsUnavailable))
		mux.HandleFunc("GET /api/v1/alerts/{id}/history", s.withPermission(auth.PermFleetRead, s.handleAlertsUnavailable))
	}

	// Scheduled jobs
	if s.jobsHandler != nil {
		mux.HandleFunc("GET /api/v1/jobs", s.withPermission(auth.PermFleetRead, s.jobsHandler.HandleListJobs))
		mux.HandleFunc("GET /api/v1/jobs/runs", s.withPermission(auth.PermFleetRead, s.jobsHandler.HandleListAllRuns))
		mux.HandleFunc("POST /api/v1/jobs", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleCreateJob))
		mux.HandleFunc("GET /api/v1/jobs/{id}", s.withPermission(auth.PermFleetRead, s.jobsHandler.HandleGetJob))
		mux.HandleFunc("PUT /api/v1/jobs/{id}", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleUpdateJob))
		mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleDeleteJob))
		mux.HandleFunc("POST /api/v1/jobs/{id}/run", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleRunJob))
		mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleCancelJob))
		mux.HandleFunc("GET /api/v1/jobs/{id}/runs", s.withPermission(auth.PermFleetRead, s.jobsHandler.HandleListRuns))
		mux.HandleFunc("POST /api/v1/jobs/{id}/runs/{runId}/cancel", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleCancelRun))
		mux.HandleFunc("POST /api/v1/jobs/{id}/runs/{runId}/retry", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleRetryRun))
		mux.HandleFunc("POST /api/v1/jobs/{id}/enable", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleEnableJob))
		mux.HandleFunc("POST /api/v1/jobs/{id}/disable", s.withPermission(auth.PermFleetWrite, s.jobsHandler.HandleDisableJob))
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

	// Kubeflow API (MVP read-only + guarded refresh action)
	if s.kubeflowHandlers != nil {
		mux.HandleFunc("GET /api/v1/kubeflow/status", s.withPermission(auth.PermFleetRead, s.kubeflowHandlers.HandleStatus))
		mux.HandleFunc("GET /api/v1/kubeflow/inventory", s.withPermission(auth.PermFleetRead, s.kubeflowHandlers.HandleInventory))
		mux.HandleFunc("POST /api/v1/kubeflow/actions/refresh", s.withPermission(auth.PermFleetWrite, s.kubeflowHandlers.HandleRefresh))
	} else {
		mux.HandleFunc("GET /api/v1/kubeflow/status", s.withPermission(auth.PermFleetRead, s.handleKubeflowUnavailable))
		mux.HandleFunc("GET /api/v1/kubeflow/inventory", s.withPermission(auth.PermFleetRead, s.handleKubeflowUnavailable))
		mux.HandleFunc("POST /api/v1/kubeflow/actions/refresh", s.withPermission(auth.PermFleetWrite, s.handleKubeflowUnavailable))
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
	} else {
		mux.HandleFunc("GET /api/v1/network/devices", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("GET /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetRead, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("PUT /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("DELETE /api/v1/network/devices/{id}", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/test", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
		mux.HandleFunc("POST /api/v1/network/devices/{id}/inventory", s.withPermission(auth.PermFleetWrite, s.handleNetworkDevicesUnavailable))
	}

	// Binary download + install script
	mux.HandleFunc("GET /download/{filename}", s.handleDownload)
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)

	// Static assets
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join("web", "static")))))

	// Landing page (testing)
	mux.Handle("GET /site/", http.StripPrefix("/site/", http.FileServer(http.Dir(filepath.Join("web", "site")))))

	// Web UI pages
	mux.HandleFunc("GET /", s.handleFleetPage)
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

	// WebSocket for probes
	mux.HandleFunc("GET /ws/probe", s.hub.HandleProbeWS)
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
	_ = json.NewEncoder(w).Encode(s.fleetMgr.List())
}

func (s *Server) handleGetProbe(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "probe not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ps)
}

func (s *Server) handleProbeHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	id := r.PathValue("id")
	ps, ok := s.fleetMgr.Get(id)
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

	var cmd protocol.CommandPayload
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	wantWait := r.URL.Query().Get("wait") == "true" || r.URL.Query().Get("wait") == "1"
	wantStream := r.URL.Query().Get("stream") == "true" || r.URL.Query().Get("stream") == "1"

	invokeInput := corecommanddispatch.AssembleCommandInvokeHTTP(id, cmd, wantWait, wantStream)
	if invokeInput == nil {
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "command dispatch failed")
		return
	}
	cmd = invokeInput.Command

	// Check if this command needs approval
	req, needsApproval, err := s.approvalCore.SubmitCommandApproval(id, &cmd, ps.PolicyLevel, "Manual command dispatch", "api")
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", fmt.Sprintf("approval queue: %s", err.Error()))
		return
	}
	if needsApproval {
		s.emitAudit(audit.EventApprovalRequest, id, "api",
			fmt.Sprintf("Approval required for: %s (risk: %s)", cmd.Command, req.RiskLevel))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "pending_approval",
			"approval_id": req.ID,
			"risk_level":  req.RiskLevel,
			"expires_at":  req.ExpiresAt,
			"message":     "Command requires human approval. Use POST /api/v1/approvals/{id}/decide to approve or deny.",
		})
		return
	}

	projection := corecommanddispatch.InvokeCommandForSurface(r.Context(), invokeInput, s.dispatchCore)
	if projection != nil && projection.Envelope != nil && projection.Envelope.Dispatched {
		s.emitAudit(audit.EventCommandSent, id, "api", fmt.Sprintf("Command dispatched: %s", cmd.Command))
		s.publishEvent(events.CommandDispatched, id, fmt.Sprintf("Command dispatched: %s", cmd.Command),
			map[string]string{"request_id": projection.RequestID, "command": cmd.Command})
	}

	renderDispatchCommandHTTP(w, projection)
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

	inv := s.fleetMgr.Inventory(fleet.InventoryFilter{
		Tag:    r.URL.Query().Get("tag"),
		Status: r.URL.Query().Get("status"),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(inv)
}

func (s *Server) handleFleetSummary(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"counts":            s.fleetMgr.Count(),
		"connected":         s.hub.Connected(),
		"pending_approvals": s.approvalQueue.PendingCount(),
	})
}

func (s *Server) handleFleetTags(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tags": s.fleetMgr.TagCounts()})
}

func (s *Server) handleListByTag(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	tag := r.PathValue("tag")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.fleetMgr.ListByTag(tag))
}

func (s *Server) handleGroupCommand(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermCommandExec) {
		return
	}
	tag := r.PathValue("tag")
	probes := s.fleetMgr.ListByTag(tag)
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

	if status == "pending" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"approvals":     s.approvalQueue.Pending(),
			"pending_count": s.approvalQueue.PendingCount(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"approvals":     s.approvalQueue.All(limit),
		"pending_count": s.approvalQueue.PendingCount(),
	})
}

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermApprovalRead) {
		return
	}
	id := r.PathValue("id")
	req, ok := s.approvalQueue.Get(id)
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

	projection := orchestrateDecideApprovalHTTP(r.Body, func(body *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		return s.approvalCore.DecideAndDispatch(id, body.Decision, body.DecidedBy, func(probeID string, cmd protocol.CommandPayload) error {
			return s.dispatchCore.Dispatch(probeID, cmd)
		})
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

	filename := fmt.Sprintf("legator-audit-%s.jsonl", time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

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

	filename := fmt.Sprintf("legator-audit-%s.csv", time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

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
	requestID := r.PathValue("requestId")
	if requestID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "request_id required")
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	if body.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name required")
		return
	}
	tpl := s.policyStore.Create(body.Name, body.Description, body.Level, body.Allowed, body.Blocked, body.Paths)
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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Legator Control Plane</title></head>
<body>
<h1>Legator Control Plane</h1>
<p>Version: %s (%s)</p>
<p><a href="/api/v1/probes">Fleet API</a> | <a href="/api/v1/fleet/summary">Summary</a> | <a href="/api/v1/approvals?status=pending">Approvals</a></p>
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
	data := FleetPageData{
		BasePage: BasePage{
			CurrentUser: s.currentTemplateUser(r),
			Version:     Version,
			ActiveNav:   "fleet",
		},
		Probes: probes,
		Summary: FleetSummary{
			Online:   counts["online"],
			Offline:  counts["offline"],
			Degraded: counts["degraded"],
			Total:    len(probes),
		},
		Commit: Commit,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages.Render(w, "fleet", data); err != nil {
		s.logger.Error("failed to render fleet page", zap.Error(err))
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

func (s *Server) handleKubeflowUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "kubeflow adapter unavailable")
}

func (s *Server) handleDiscoveryUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "discovery unavailable")
}
