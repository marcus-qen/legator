/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package dashboard provides a read-only web dashboard for Legator.
// It displays agent fleet status, run history, audit trails, and metrics.
// Authentication is via OIDC.
package dashboard

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Config holds dashboard server configuration.
type Config struct {
	// ListenAddr is the address to listen on (e.g. ":8080").
	ListenAddr string

	// BasePath is the URL prefix (e.g. "/dashboard" or "").
	BasePath string

	// Namespace filters agents to a specific namespace (empty = all).
	Namespace string

	// APIBaseURL is the Legator API endpoint used for approval decisions from the UI.
	// Example: http://127.0.0.1:8090
	APIBaseURL string

	// OIDC configuration
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
}

// Server is the dashboard HTTP server.
type Server struct {
	client     client.Client
	config     Config
	log        logr.Logger
	pages      map[string]*template.Template
	mux        *http.ServeMux
	oidc       *OIDCMiddleware
	httpClient *http.Client
}

// NewServer creates a new dashboard server.
func NewServer(c client.Client, cfg Config, log logr.Logger) (*Server, error) {
	funcMap := template.FuncMap{
		"timeAgo":       timeAgo,
		"formatTime":    formatTime,
		"truncate":      truncateStr,
		"statusIcon":    statusIcon,
		"severityClass": severityClass,
		"pct":           pct,
		"durationMs":    durationMs,
		"tokensStr":     tokensStr,
		"gateOutcome":   gateOutcome,
	}

	// Parse layout as the base template, then clone for each page.
	// This avoids the "last {{define "content"}} wins" problem.
	layoutTmpl, err := template.New("layout.html").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}

	pages := []string{
		"index.html", "cockpit.html", "agents.html", "agent-detail.html",
		"runs.html", "run-detail.html",
		"approvals.html", "events.html",
	}

	templates := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		// Clone the layout for each page so "content" definitions don't collide
		t, err := layoutTmpl.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone layout for %s: %w", page, err)
		}
		_, err = t.ParseFS(templateFS, "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		templates[page] = t
	}

	s := &Server{
		client:     c,
		config:     cfg,
		log:        log,
		pages:      templates,
		mux:        http.NewServeMux(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	s.registerRoutes()

	// Wire OIDC middleware if configured
	if cfg.OIDCIssuer != "" && cfg.OIDCClientID != "" {
		s.oidc = NewOIDCMiddleware(OIDCConfig{
			IssuerURL:    cfg.OIDCIssuer,
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			RedirectURL:  cfg.OIDCRedirectURL,
		})
		log.Info("OIDC authentication enabled",
			"issuer", cfg.OIDCIssuer,
			"clientID", cfg.OIDCClientID,
			"redirectURL", cfg.OIDCRedirectURL,
		)
	}

	return s, nil
}

// registerRoutes sets up the HTTP routing.
func (s *Server) registerRoutes() {
	base := strings.TrimRight(s.config.BasePath, "/")

	// Static assets
	staticSub, _ := fs.Sub(staticFS, "static")
	s.mux.Handle(base+"/static/", http.StripPrefix(base+"/static/", http.FileServer(http.FS(staticSub))))

	// Pages
	s.mux.HandleFunc(base+"/", s.handleIndex)
	s.mux.HandleFunc(base+"/cockpit", s.handleCockpit)
	s.mux.HandleFunc(base+"/cockpit/missions", s.handleMissionLaunch)
	s.mux.HandleFunc(base+"/agents", s.handleAgents)
	s.mux.HandleFunc(base+"/agents/", s.handleAgentDetail)
	s.mux.HandleFunc(base+"/runs", s.handleRuns)
	s.mux.HandleFunc(base+"/runs/", s.handleRunDetail)
	s.mux.HandleFunc(base+"/approvals", s.handleApprovals)
	s.mux.HandleFunc(base+"/approvals/", s.handleApprovalAction)
	s.mux.HandleFunc(base+"/events", s.handleEvents)

	// API endpoints for htmx partials
	s.mux.HandleFunc(base+"/api/agents-table", s.handleAgentsTable)
	s.mux.HandleFunc(base+"/api/runs-table", s.handleRunsTable)
	s.mux.HandleFunc(base+"/api/approvals-count", s.handleApprovalsCount)
	s.mux.HandleFunc(base+"/api/cockpit/topology", s.handleCockpitTopology)
	s.mux.HandleFunc(base+"/api/cockpit/approvals", s.handleCockpitApprovals)
	s.mux.HandleFunc(base+"/api/cockpit/timeline", s.handleCockpitTimeline)
	s.mux.HandleFunc(base+"/api/cockpit/connectivity", s.handleCockpitConnectivity)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handler returns the top-level HTTP handler, wrapping with OIDC if configured.
func (s *Server) handler() http.Handler {
	var h http.Handler = s
	if s.oidc != nil {
		h = s.oidc.Wrap(h)
	}
	return h
}

// Start runs the dashboard server.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.config.ListenAddr,
		Handler: s.handler(),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	s.log.Info("Dashboard server starting", "addr", s.config.ListenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// --- Page Handlers ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != s.config.BasePath+"/" && r.URL.Path != s.config.BasePath {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	data := s.buildOverviewData(ctx)
	s.render(w, "index.html", data)
}

func (s *Server) handleCockpit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agents := s.listAgents(ctx)
	data := map[string]any{
		"Title":  "Cockpit",
		"Agents": agents,
	}
	s.render(w, "cockpit.html", data)
}

func (s *Server) handleMissionLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(s.config.APIBaseURL) == "" {
		http.Error(w, "Mission launch disabled: dashboard API bridge not configured", http.StatusServiceUnavailable)
		return
	}

	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthenticated", http.StatusUnauthorized)
		return
	}

	agentName := strings.TrimSpace(r.FormValue("agent"))
	intent := strings.TrimSpace(r.FormValue("intent"))
	target := strings.TrimSpace(r.FormValue("target"))
	safetyMode := strings.TrimSpace(r.FormValue("safetyMode"))
	if agentName == "" {
		http.Error(w, "agent is required", http.StatusBadRequest)
		return
	}
	if intent == "" {
		intent = "Operator-triggered mission from cockpit"
	}

	autonomy := "observe"
	switch safetyMode {
	case "", "observe", "read-only":
		autonomy = "observe"
	case "recommend":
		autonomy = "recommend"
	case "automate-safe":
		autonomy = "automate-safe"
	}

	launchStart := time.Now()
	if err := s.launchMissionViaAPI(r.Context(), user, agentName, intent, target, autonomy); err != nil {
		var apiErr *approvalAPIError
		if errors.As(err, &apiErr) {
			http.Error(w, apiErr.Error(), apiErr.StatusCode)
			return
		}
		s.log.Error(err, "Failed to launch mission via API", "agent", agentName)
		http.Error(w, "Failed to launch mission", http.StatusInternalServerError)
		return
	}

	runID := s.awaitRunForAgent(r.Context(), agentName, launchStart, 5*time.Second)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if runID == "" {
		fmt.Fprint(w, `<div class="mission-result warn">Mission accepted. Waiting for run ID...</div>`)
		return
	}
	fmt.Fprintf(w, `<div class="mission-result ok">Mission launched for <code>%s</code>. Run ID: <a href="/runs/%s"><code>%s</code></a></div>`,
		template.HTMLEscapeString(agentName),
		url.PathEscape(runID),
		template.HTMLEscapeString(runID),
	)
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agents := s.listAgents(ctx)
	s.render(w, "agents.html", map[string]interface{}{
		"Agents": agents,
		"Title":  "Agents",
	})
}

func (s *Server) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimPrefix(r.URL.Path, s.config.BasePath+"/agents/")
	if name == "" {
		http.Redirect(w, r, s.config.BasePath+"/agents", http.StatusFound)
		return
	}

	agent, runs := s.getAgentDetail(ctx, name)
	if agent == nil {
		http.NotFound(w, r)
		return
	}

	s.render(w, "agent-detail.html", map[string]interface{}{
		"Agent": agent,
		"Runs":  runs,
		"Title": agent.Name,
	})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agentFilter := r.URL.Query().Get("agent")
	runs := s.listRuns(ctx, agentFilter, 50)
	s.render(w, "runs.html", map[string]interface{}{
		"Runs":        runs,
		"AgentFilter": agentFilter,
		"Title":       "Runs",
	})
}

func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimPrefix(r.URL.Path, s.config.BasePath+"/runs/")
	if name == "" {
		http.Redirect(w, r, s.config.BasePath+"/runs", http.StatusFound)
		return
	}

	run := s.getRun(ctx, name)
	if run == nil {
		http.NotFound(w, r)
		return
	}

	s.render(w, "run-detail.html", map[string]interface{}{
		"Run":   run,
		"Title": run.Name,
	})
}

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	approvals := s.listApprovals(ctx)
	s.render(w, "approvals.html", map[string]interface{}{
		"Approvals": approvals,
		"Title":     "Approvals",
	})
}

func (s *Server) handleApprovalAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, s.config.BasePath+"/approvals/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	name, action := parts[0], parts[1]
	reason := r.FormValue("reason")

	if action != "approve" && action != "deny" {
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(s.config.APIBaseURL) == "" {
		http.Error(w, "Approval actions disabled: dashboard API bridge not configured", http.StatusServiceUnavailable)
		return
	}

	user := UserFromContext(ctx)
	if user == nil {
		http.Error(w, "Unauthenticated", http.StatusUnauthorized)
		return
	}

	if err := s.decideApprovalViaAPI(ctx, user, name, action, reason); err != nil {
		var apiErr *approvalAPIError
		if errors.As(err, &apiErr) {
			http.Error(w, apiErr.Error(), apiErr.StatusCode)
			return
		}
		s.log.Error(err, "Failed to update approval via API", "name", name, "action", action)
		http.Error(w, "Failed to update approval", http.StatusInternalServerError)
		return
	}

	redirectTo := s.config.BasePath + "/approvals"
	if strings.Contains(r.Referer(), "/cockpit") {
		redirectTo = s.config.BasePath + "/cockpit"
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	events := s.listEvents(ctx)
	s.render(w, "events.html", map[string]interface{}{
		"Events": events,
		"Title":  "Events",
	})
}

type topologyNode struct {
	Name   string
	Kind   string
	Status string
	Detail string
}

type timelineEntry struct {
	RunName     string
	Agent       string
	Tool        string
	Target      string
	Tier        string
	GateOutcome string
	Time        time.Time
}

func (s *Server) handleCockpitTopology(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodes := make([]topologyNode, 0)

	devices, err := s.fetchInventoryViaAPI(ctx, UserFromContext(ctx))
	if err == nil {
		for _, device := range devices {
			status := strings.TrimSpace(device.Status)
			if status == "" {
				status = "unknown"
			}
			detail := strings.TrimSpace(device.IP)
			if detail == "" {
				detail = strings.TrimSpace(device.URL)
			}
			if detail == "" {
				detail = "no address"
			}
			nodes = append(nodes, topologyNode{
				Name:   device.Name,
				Kind:   "endpoint",
				Status: status,
				Detail: detail,
			})
		}
	}

	agents := s.listAgents(ctx)
	for _, agent := range agents {
		status := strings.TrimSpace(string(agent.Status.Phase))
		if status == "" {
			status = "Unknown"
		}
		detail := strings.TrimSpace(agent.Spec.EnvironmentRef)
		if detail == "" {
			detail = "default env"
		}
		nodes = append(nodes, topologyNode{
			Name:   agent.Name,
			Kind:   "agent",
			Status: status,
			Detail: detail,
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Kind == nodes[j].Kind {
			return nodes[i].Name < nodes[j].Name
		}
		return nodes[i].Kind < nodes[j].Kind
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(nodes) == 0 {
		fmt.Fprint(w, `<div class="empty">No topology data yet</div>`)
		return
	}

	fmt.Fprint(w, `<div class="topology-grid">`)
	for _, node := range nodes {
		fmt.Fprintf(w, `<div class="topology-node"><div class="topology-title">%s</div><div class="topology-meta">%s • %s</div><div class="topology-detail">%s</div></div>`,
			template.HTMLEscapeString(node.Name),
			template.HTMLEscapeString(node.Kind),
			template.HTMLEscapeString(node.Status),
			template.HTMLEscapeString(node.Detail),
		)
	}
	fmt.Fprint(w, `</div>`)
}

func (s *Server) handleCockpitApprovals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	approvals := s.listApprovals(ctx)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	pending := 0
	for _, approval := range approvals {
		if approval.Status.Phase != corev1alpha1.ApprovalPhasePending {
			continue
		}
		pending++
		fmt.Fprintf(w, `<tr><td><code>%s</code></td><td>%s</td><td><code>%s</code></td><td>%s</td><td>
<form method="POST" action="/approvals/%s/approve" style="display:inline"><input type="hidden" name="reason" value="cockpit-approved"><button class="btn btn-approve">Approve</button></form>
<form method="POST" action="/approvals/%s/deny" style="display:inline"><input type="hidden" name="reason" value="cockpit-denied"><button class="btn btn-deny">Deny</button></form>
</td></tr>`,
			template.HTMLEscapeString(approval.Name),
			template.HTMLEscapeString(approval.Spec.AgentName),
			template.HTMLEscapeString(approval.Spec.RunName),
			template.HTMLEscapeString(timeAgo(approval.CreationTimestamp.Time)),
			url.PathEscape(approval.Name),
			url.PathEscape(approval.Name),
		)
	}

	if pending == 0 {
		fmt.Fprint(w, `<tr><td colspan="5" class="empty">No pending approval requests</td></tr>`)
	}
}

func (s *Server) handleCockpitConnectivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.fetchCockpitConnectivityViaAPI(ctx, UserFromContext(ctx), 12)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		fmt.Fprintf(w, `<tr><td colspan="10" class="empty">Connectivity feed unavailable: %s</td></tr>`, template.HTMLEscapeString(err.Error()))
		return
	}
	if len(rows) == 0 {
		fmt.Fprint(w, `<tr><td colspan="10" class="empty">No mission connectivity data yet</td></tr>`)
		return
	}

	now := time.Now()
	for _, row := range rows {
		tunnelTTL := ttlRemaining(row.tunnelExpiryTime(), now)
		if tunnelTTL == "—" && row.Tunnel.LeaseTTLSeconds > 0 {
			tunnelTTL = fmt.Sprintf("%ds", row.Tunnel.LeaseTTLSeconds)
		}

		credMode := strings.TrimSpace(row.Credential.Mode)
		if credMode == "" {
			credMode = "none"
		}
		issuer := strings.TrimSpace(row.Credential.Issuer)
		if issuer == "" {
			issuer = "—"
		}
		credTTL := ttlRemaining(row.credentialExpiryTime(), now)
		if credTTL == "—" && row.Credential.TTLSeconds > 0 {
			credTTL = fmt.Sprintf("%ds", row.Credential.TTLSeconds)
		}
		riskLabel, riskClass := credentialRisk(credMode, credTTL)

		fmt.Fprintf(w, `<tr><td><a href="/runs/%s"><code>%s</code></a></td><td><code>%s</code></td><td><span class="status-pill %s">%s</span></td><td>%s</td><td>%s</td><td><span class="credential-pill %s">%s</span></td><td><code>%s</code></td><td>%s</td><td><span class="risk-pill %s">%s</span></td><td>%s</td></tr>`,
			url.PathEscape(row.Run),
			template.HTMLEscapeString(row.Run),
			template.HTMLEscapeString(row.Tunnel.Provider),
			template.HTMLEscapeString(tunnelStatusClass(row.Tunnel.Status)),
			template.HTMLEscapeString(row.Tunnel.Status),
			template.HTMLEscapeString(truncateStr(row.Tunnel.Target, 30)),
			template.HTMLEscapeString(tunnelTTL),
			template.HTMLEscapeString(credentialClass(credMode)),
			template.HTMLEscapeString(credMode),
			template.HTMLEscapeString(truncateStr(issuer, 36)),
			template.HTMLEscapeString(credTTL),
			template.HTMLEscapeString(riskClass),
			template.HTMLEscapeString(riskLabel),
			template.HTMLEscapeString(timeAgo(row.LastTransitionTime())),
		)
	}
}

func (s *Server) handleCockpitTimeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries := s.buildTimelineEntries(ctx, 20)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if len(entries) == 0 {
		fmt.Fprint(w, `<tr><td colspan="9" class="empty">No run timeline data yet</td></tr>`)
		return
	}

	connectivityByRun := map[string]cockpitConnectivityRun{}
	if rows, err := s.fetchCockpitConnectivityViaAPI(ctx, UserFromContext(ctx), 64); err == nil {
		for _, row := range rows {
			if _, exists := connectivityByRun[row.Run]; exists {
				continue
			}
			connectivityByRun[row.Run] = row
		}
	}

	for _, entry := range entries {
		tunnelPath, credMode := timelineAttribution(entry.RunName, connectivityByRun)
		fmt.Fprintf(w, `<tr><td><a href="/runs/%s"><code>%s</code></a></td><td>%s</td><td><code>%s</code></td><td>%s</td><td><code>%s</code></td><td>%s</td><td><code>%s</code></td><td><span class="credential-pill %s">%s</span></td><td>%s</td></tr>`,
			url.PathEscape(entry.RunName),
			template.HTMLEscapeString(entry.RunName),
			template.HTMLEscapeString(entry.Agent),
			template.HTMLEscapeString(entry.Tool),
			template.HTMLEscapeString(truncateStr(entry.Target, 34)),
			template.HTMLEscapeString(entry.Tier),
			template.HTMLEscapeString(entry.GateOutcome),
			template.HTMLEscapeString(truncateStr(tunnelPath, 34)),
			template.HTMLEscapeString(credentialClass(credMode)),
			template.HTMLEscapeString(credMode),
			template.HTMLEscapeString(timeAgo(entry.Time)),
		)
	}
}

// --- htmx API Handlers ---

func (s *Server) handleAgentsTable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agents := s.listAgents(ctx)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, a := range agents {
		phase := string(a.Status.Phase)
		sched := string(a.Spec.Schedule.Cron)
		if sched == "" {
			sched = a.Spec.Schedule.Interval
		}
		lastRun := "never"
		if a.Status.LastRunTime != nil {
			lastRun = timeAgo(a.Status.LastRunTime.Time)
		}
		fmt.Fprintf(w, "<tr><td>%s</td><td><a href=\"/agents/%s\">%s</a></td><td><code>%s</code></td><td>%s</td><td>%d</td><td>%d</td><td>%s</td></tr>\n",
			statusIcon(phase), a.Name, a.Name, a.Spec.Guardrails.Autonomy, sched, a.Status.RunCount, a.Status.ConsecutiveFailures, lastRun)
	}
}

func (s *Server) handleRunsTable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agentFilter := r.URL.Query().Get("agent")
	runs := s.listRuns(ctx, agentFilter, 20)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, run := range runs {
		phase := string(run.Status.Phase)
		tokens := "—"
		dur := "—"
		if run.Status.Usage != nil {
			tokens = tokensStr(run.Status.Usage.TokensIn, run.Status.Usage.TokensOut)
			dur = durationMs(run.Status.Usage.WallClockMs)
		}
		fmt.Fprintf(w, "<tr><td>%s</td><td><a href=\"/runs/%s\">%s</a></td><td><a href=\"/agents/%s\">%s</a></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			statusIcon(phase), run.Name, run.Name, run.Spec.AgentRef, run.Spec.AgentRef, run.Spec.Trigger, tokens, dur, timeAgo(run.CreationTimestamp.Time))
	}
}

func (s *Server) handleApprovalsCount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	approvals := s.listApprovals(ctx)
	pending := 0
	for _, a := range approvals {
		if a.Status.Phase == corev1alpha1.ApprovalPhasePending {
			pending++
		}
	}
	fmt.Fprintf(w, "%d", pending)
}

// --- Data Fetchers ---

func (s *Server) buildOverviewData(ctx context.Context) map[string]interface{} {
	agents := s.listAgents(ctx)
	runs := s.listRuns(ctx, "", 10)
	approvals := s.listApprovals(ctx)

	readyCount := 0
	for _, a := range agents {
		if a.Status.Phase == "Ready" {
			readyCount++
		}
	}

	pendingApprovals := 0
	for _, a := range approvals {
		if a.Status.Phase == corev1alpha1.ApprovalPhasePending {
			pendingApprovals++
		}
	}

	// Calculate success rate from recent runs
	allRuns := s.listRuns(ctx, "", 100)
	succeeded, total := 0, len(allRuns)
	var totalTokens int64
	for _, r := range allRuns {
		if r.Status.Phase == corev1alpha1.RunPhaseSucceeded {
			succeeded++
		}
		if r.Status.Usage != nil {
			totalTokens += r.Status.Usage.TokensIn + r.Status.Usage.TokensOut
		}
	}

	successRate := 0.0
	if total > 0 {
		successRate = float64(succeeded) / float64(total) * 100
	}

	return map[string]interface{}{
		"Title":            "Dashboard",
		"Agents":           agents,
		"AgentCount":       len(agents),
		"ReadyCount":       readyCount,
		"RecentRuns":       runs,
		"TotalRuns":        total,
		"SuccessRate":      successRate,
		"TotalTokens":      totalTokens,
		"PendingApprovals": pendingApprovals,
	}
}

func (s *Server) listAgents(ctx context.Context) []corev1alpha1.LegatorAgent {
	list := &corev1alpha1.LegatorAgentList{}
	opts := []client.ListOption{}
	if s.config.Namespace != "" {
		opts = append(opts, client.InNamespace(s.config.Namespace))
	}
	if err := s.client.List(ctx, list, opts...); err != nil {
		s.log.Error(err, "Failed to list agents")
		return nil
	}

	// Sort by name
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})

	return list.Items
}

func (s *Server) listRuns(ctx context.Context, agentFilter string, limit int) []corev1alpha1.LegatorRun {
	list := &corev1alpha1.LegatorRunList{}
	opts := []client.ListOption{}
	if s.config.Namespace != "" {
		opts = append(opts, client.InNamespace(s.config.Namespace))
	}
	if err := s.client.List(ctx, list, opts...); err != nil {
		s.log.Error(err, "Failed to list runs")
		return nil
	}

	// Filter by agent if specified
	var items []corev1alpha1.LegatorRun
	for _, r := range list.Items {
		if agentFilter != "" && r.Spec.AgentRef != agentFilter {
			continue
		}
		items = append(items, r)
	}

	// Sort by creation time (newest first)
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreationTimestamp.After(items[j].CreationTimestamp.Time)
	})

	if len(items) > limit {
		items = items[:limit]
	}

	return items
}

func (s *Server) getRun(ctx context.Context, name string) *corev1alpha1.LegatorRun {
	run := &corev1alpha1.LegatorRun{}
	ns := s.config.Namespace
	if ns == "" {
		ns = "agents" // sensible default
	}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, run); err != nil {
		return nil
	}
	return run
}

func (s *Server) getAgentDetail(ctx context.Context, name string) (*corev1alpha1.LegatorAgent, []corev1alpha1.LegatorRun) {
	agent := &corev1alpha1.LegatorAgent{}
	ns := s.config.Namespace
	if ns == "" {
		ns = "agents"
	}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, agent); err != nil {
		return nil, nil
	}

	runs := s.listRuns(ctx, name, 20)
	return agent, runs
}

func (s *Server) listApprovals(ctx context.Context) []corev1alpha1.ApprovalRequest {
	list := &corev1alpha1.ApprovalRequestList{}
	opts := []client.ListOption{}
	if s.config.Namespace != "" {
		opts = append(opts, client.InNamespace(s.config.Namespace))
	}
	if err := s.client.List(ctx, list, opts...); err != nil {
		s.log.Error(err, "Failed to list approvals")
		return nil
	}

	// Sort: pending first, then by creation time
	sort.Slice(list.Items, func(i, j int) bool {
		if list.Items[i].Status.Phase == corev1alpha1.ApprovalPhasePending &&
			list.Items[j].Status.Phase != corev1alpha1.ApprovalPhasePending {
			return true
		}
		if list.Items[i].Status.Phase != corev1alpha1.ApprovalPhasePending &&
			list.Items[j].Status.Phase == corev1alpha1.ApprovalPhasePending {
			return false
		}
		return list.Items[i].CreationTimestamp.After(list.Items[j].CreationTimestamp.Time)
	})

	return list.Items
}

type approvalAPIError struct {
	StatusCode int
	Body       string
}

func (e *approvalAPIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("approval api %d", e.StatusCode)
	}
	return fmt.Sprintf("approval api %d: %s", e.StatusCode, body)
}

func (s *Server) decideApprovalViaAPI(ctx context.Context, user *OIDCUser, name, decision, reason string) error {
	payload := map[string]string{"decision": decision}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = reason
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode approval payload: %w", err)
	}

	apiURL := strings.TrimRight(s.config.APIBaseURL, "/") + "/api/v1/approvals/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build approval api request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+makeDashboardJWT(user))

	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("approval api request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusOK {
		return &approvalAPIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	return nil
}

func makeDashboardJWT(user *OIDCUser) string {
	claims := map[string]any{
		"sub":   user.Subject,
		"email": user.Email,
		"name":  user.Name,
		"exp":   time.Now().Add(5 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
	}
	if len(user.Groups) > 0 {
		claims["groups"] = user.Groups
	}
	if strings.TrimSpace(user.PreferredUser) != "" {
		claims["preferred_username"] = user.PreferredUser
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf("%s.%s.dashboard", header, body)
}

func (s *Server) launchMissionViaAPI(ctx context.Context, user *OIDCUser, agentName, intent, target, autonomy string) error {
	payload := map[string]string{
		"task": intent,
	}
	if strings.TrimSpace(target) != "" {
		payload["target"] = target
	}
	if strings.TrimSpace(autonomy) != "" {
		payload["autonomy"] = autonomy
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode mission payload: %w", err)
	}

	apiURL := strings.TrimRight(s.config.APIBaseURL, "/") + "/api/v1/agents/" + url.PathEscape(agentName) + "/run"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build mission api request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+makeDashboardJWT(user))

	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mission api request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return &approvalAPIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	return nil
}

func (s *Server) awaitRunForAgent(ctx context.Context, agentName string, since time.Time, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ""
		default:
		}

		runs := s.listRuns(ctx, agentName, 5)
		for _, run := range runs {
			if run.CreationTimestamp.Time.After(since.Add(-2 * time.Second)) {
				return run.Name
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ""
}

type inventoryDevice struct {
	Name   string `json:"name"`
	IP     string `json:"ip,omitempty"`
	URL    string `json:"url,omitempty"`
	Status string `json:"status,omitempty"`
}

type cockpitConnectivityRun struct {
	Run         string `json:"run"`
	Agent       string `json:"agent"`
	Phase       string `json:"phase"`
	Environment string `json:"environment"`
	Tunnel      struct {
		Status           string `json:"status"`
		Provider         string `json:"provider"`
		RouteID          string `json:"routeId"`
		Target           string `json:"target"`
		LeaseTTLSeconds  int64  `json:"leaseTtlSeconds"`
		ExpiresAt        string `json:"expiresAt"`
		LastTransitionAt string `json:"lastTransitionAt"`
	} `json:"tunnel"`
	Credential struct {
		Mode       string `json:"mode"`
		Issuer     string `json:"issuer"`
		TTLSeconds int64  `json:"ttlSeconds"`
		ExpiresAt  string `json:"expiresAt"`
	} `json:"credential"`
}

func (r cockpitConnectivityRun) LastTransitionTime() time.Time {
	if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Tunnel.LastTransitionAt)); err == nil {
		return ts
	}
	return time.Now()
}

func (r cockpitConnectivityRun) tunnelExpiryTime() time.Time {
	if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Tunnel.ExpiresAt)); err == nil {
		return ts
	}
	return time.Time{}
}

func (r cockpitConnectivityRun) credentialExpiryTime() time.Time {
	if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Credential.ExpiresAt)); err == nil {
		return ts
	}
	return time.Time{}
}

func (s *Server) fetchInventoryViaAPI(ctx context.Context, user *OIDCUser) ([]inventoryDevice, error) {
	if strings.TrimSpace(s.config.APIBaseURL) == "" || user == nil {
		return nil, errors.New("api bridge unavailable")
	}

	apiURL := strings.TrimRight(s.config.APIBaseURL, "/") + "/api/v1/inventory"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+makeDashboardJWT(user))

	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inventory api status %d", resp.StatusCode)
	}

	var payload struct {
		Devices []inventoryDevice `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Devices, nil
}

func (s *Server) fetchCockpitConnectivityViaAPI(ctx context.Context, user *OIDCUser, limit int) ([]cockpitConnectivityRun, error) {
	if strings.TrimSpace(s.config.APIBaseURL) == "" || user == nil {
		return nil, errors.New("api bridge unavailable")
	}
	if limit <= 0 {
		limit = 12
	}

	apiURL := fmt.Sprintf("%s/api/v1/cockpit/connectivity?limit=%d", strings.TrimRight(s.config.APIBaseURL, "/"), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+makeDashboardJWT(user))

	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("connectivity api status %d", resp.StatusCode)
	}

	var payload struct {
		Runs []cockpitConnectivityRun `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Runs, nil
}

func (s *Server) buildTimelineEntries(ctx context.Context, limit int) []timelineEntry {
	runs := s.listRuns(ctx, "", 12)
	entries := make([]timelineEntry, 0, limit)

	for _, run := range runs {
		if len(run.Status.Actions) == 0 {
			entries = append(entries, timelineEntry{
				RunName:     run.Name,
				Agent:       run.Spec.AgentRef,
				Tool:        "run",
				Target:      string(run.Spec.Trigger),
				Tier:        "n/a",
				GateOutcome: gateOutcome(strings.ToLower(string(run.Status.Phase))),
				Time:        run.CreationTimestamp.Time,
			})
			if len(entries) >= limit {
				break
			}
			continue
		}

		for _, action := range run.Status.Actions {
			entry := timelineEntry{
				RunName:     run.Name,
				Agent:       run.Spec.AgentRef,
				Tool:        action.Tool,
				Target:      action.Target,
				Tier:        string(action.Tier),
				GateOutcome: gateOutcome(string(action.Status)),
				Time:        action.Timestamp.Time,
			}
			if entry.Time.IsZero() {
				entry.Time = run.CreationTimestamp.Time
			}
			entries = append(entries, entry)
			if len(entries) >= limit {
				break
			}
		}
		if len(entries) >= limit {
			break
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Time.After(entries[j].Time)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

func gateOutcome(status string) string {
	switch strings.TrimSpace(status) {
	case "approved":
		return "approved"
	case "denied":
		return "denied"
	case "pending-approval", "pending", "running":
		return "pending"
	case "blocked", "failed", "skipped", "escalated":
		return "blocked"
	case "executed", "succeeded":
		return "allowed"
	default:
		return "allowed"
	}
}

func (s *Server) listEvents(ctx context.Context) []corev1alpha1.AgentEvent {
	list := &corev1alpha1.AgentEventList{}
	opts := []client.ListOption{}
	if s.config.Namespace != "" {
		opts = append(opts, client.InNamespace(s.config.Namespace))
	}
	if err := s.client.List(ctx, list, opts...); err != nil {
		s.log.Error(err, "Failed to list events")
		return nil
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].CreationTimestamp.After(list.Items[j].CreationTimestamp.Time)
	})

	return list.Items
}

// --- Render ---

func (s *Server) render(w http.ResponseWriter, name string, data interface{}) {
	t, ok := s.pages[name]
	if !ok {
		s.log.Error(nil, "Template not found", "template", name)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		s.log.Error(err, "Template render error", "template", name)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// --- Template Functions ---

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04:05")
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func tunnelStatusClass(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "approved", "allowed":
		return "status-active"
	case "establishing", "pending", "requested":
		return "status-pending"
	case "expired":
		return "status-expired"
	case "failed", "blocked", "denied":
		return "status-failed"
	default:
		return "status-unknown"
	}
}

func credentialClass(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "vault-signed-cert":
		return "credential-strong"
	case "otp":
		return "credential-otp"
	case "static-key-legacy":
		return "credential-legacy"
	default:
		return "credential-none"
	}
}

func credentialRisk(mode, ttl string) (label, class string) {
	m := strings.ToLower(strings.TrimSpace(mode))
	t := strings.ToLower(strings.TrimSpace(ttl))

	if t == "expired" {
		return "high", "risk-high"
	}
	switch m {
	case "vault-signed-cert":
		if strings.HasSuffix(t, "s") || t == "1m" {
			return "medium", "risk-medium"
		}
		return "low", "risk-low"
	case "otp":
		if strings.HasSuffix(t, "s") || t == "1m" {
			return "high", "risk-high"
		}
		return "medium", "risk-medium"
	case "static-key-legacy":
		return "high", "risk-high"
	default:
		return "unknown", "risk-unknown"
	}
}

func timelineAttribution(runName string, byRun map[string]cockpitConnectivityRun) (tunnelPath, credentialMode string) {
	row, ok := byRun[runName]
	if !ok {
		return "—", "none"
	}
	tunnelPath = strings.TrimSpace(row.Tunnel.RouteID)
	if tunnelPath == "" {
		tunnelPath = "run/" + runName
	}
	credentialMode = strings.TrimSpace(row.Credential.Mode)
	if credentialMode == "" {
		credentialMode = "none"
	}
	return tunnelPath, credentialMode
}

func ttlRemaining(expiry, now time.Time) string {
	if expiry.IsZero() {
		return "—"
	}
	d := expiry.Sub(now)
	if d <= 0 {
		return "expired"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func statusIcon(phase string) string {
	switch phase {
	case "Succeeded":
		return "✅"
	case "Failed":
		return "❌"
	case "Running":
		return "🔄"
	case "Blocked":
		return "🚫"
	case "Escalated":
		return "⚠️"
	case "Ready":
		return "✅"
	case "Pending":
		return "⏳"
	case "Approved":
		return "✅"
	case "Denied":
		return "❌"
	case "Expired":
		return "⏰"
	default:
		return "❓"
	}
}

func severityClass(severity string) string {
	switch severity {
	case "critical":
		return "severity-critical"
	case "warning":
		return "severity-warning"
	case "info":
		return "severity-info"
	default:
		return ""
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func durationMs(ms int64) string {
	if ms == 0 {
		return "—"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

func tokensStr(in, out int64) string {
	total := in + out
	if total == 0 {
		return "—"
	}
	if total > 1000 {
		return fmt.Sprintf("%.1fK", float64(total)/1000)
	}
	return fmt.Sprintf("%d", total)
}
