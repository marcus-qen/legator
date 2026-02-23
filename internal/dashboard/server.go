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
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// OIDC configuration
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
}

// Server is the dashboard HTTP server.
type Server struct {
	client client.Client
	config Config
	log    logr.Logger
	pages  map[string]*template.Template
	mux    *http.ServeMux
	oidc   *OIDCMiddleware
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
	}

	// Parse layout as the base template, then clone for each page.
	// This avoids the "last {{define "content"}} wins" problem.
	layoutTmpl, err := template.New("layout.html").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}

	pages := []string{
		"index.html", "agents.html", "agent-detail.html",
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
		client: c,
		config: cfg,
		log:    log,
		pages:  templates,
		mux:    http.NewServeMux(),
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
	path := strings.TrimPrefix(r.URL.Path, s.config.BasePath+"/agents/")
	if path == "" {
		http.Redirect(w, r, s.config.BasePath+"/agents", http.StatusFound)
		return
	}

	// Explicit ad-hoc run action endpoint: /agents/{name}/run
	if strings.HasSuffix(path, "/run") {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		agentName := strings.TrimSuffix(path, "/run")
		if strings.TrimSpace(agentName) == "" || strings.Contains(agentName, "/") {
			http.NotFound(w, r)
			return
		}

		s.handleAgentRunTrigger(w, r, agentName)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agent, runs := s.getAgentDetail(ctx, path)
	if agent == nil {
		http.NotFound(w, r)
		return
	}

	s.render(w, "agent-detail.html", map[string]interface{}{
		"Agent":     agent,
		"Runs":      runs,
		"Title":     agent.Name,
		"Triggered": r.URL.Query().Get("triggered") == "1",
	})
}

func (s *Server) handleAgentRunTrigger(w http.ResponseWriter, r *http.Request, name string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	task := strings.TrimSpace(r.FormValue("task"))
	target := strings.TrimSpace(r.FormValue("target"))

	triggeredBy := approvalActorFromContext(r.Context())
	if triggeredBy == "dashboard-user" {
		triggeredBy = ""
	}

	if err := s.triggerAdHocRun(r.Context(), name, task, target, triggeredBy); err != nil {
		s.log.Error(err, "Failed to trigger agent run", "agent", name)
		http.Error(w, "Failed to trigger agent run", http.StatusInternalServerError)
		return
	}

	location := s.config.BasePath + "/agents/" + name + "?triggered=1"
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func (s *Server) triggerAdHocRun(ctx context.Context, name string, task string, target string, triggeredBy string) error {
	agent := &corev1alpha1.LegatorAgent{}
	ns := s.config.Namespace
	if ns == "" {
		ns = "agents"
	}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, agent); err != nil {
		return err
	}

	annotations := agent.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["legator.io/run-now"] = "true"

	if task != "" {
		annotations["legator.io/task"] = task
	} else {
		delete(annotations, "legator.io/task")
	}

	if target != "" {
		annotations["legator.io/target"] = target
	} else {
		delete(annotations, "legator.io/target")
	}

	if triggeredBy != "" {
		annotations["legator.io/triggered-by"] = triggeredBy
	} else {
		delete(annotations, "legator.io/triggered-by")
	}

	agent.SetAnnotations(annotations)
	return s.client.Update(ctx, agent)
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

	pendingApprovals := 0
	for _, approval := range approvals {
		if approval.Status.Phase == corev1alpha1.ApprovalPhasePending {
			pendingApprovals++
		}
	}

	s.render(w, "approvals.html", map[string]interface{}{
		"Approvals":       approvals,
		"PendingApprovals": pendingApprovals,
		"Title":           "Approvals",
	})
}

func (s *Server) handleApprovalAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, s.config.BasePath+"/approvals/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(parts[0])
	action := strings.TrimSpace(parts[1])
	reason := strings.TrimSpace(r.FormValue("reason"))
	if name == "" || action == "" {
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}

	var phase corev1alpha1.ApprovalRequestPhase
	switch action {
	case "approve":
		phase = corev1alpha1.ApprovalPhaseApproved
	case "deny":
		phase = corev1alpha1.ApprovalPhaseDenied
	default:
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}

	actor := approvalActorFromContext(ctx)
	if err := s.updateApproval(ctx, name, phase, actor, reason); err != nil {
		s.log.Error(err, "Failed to update approval", "name", name, "action", action)
		http.Error(w, "Failed to update approval", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, s.config.BasePath+"/approvals", http.StatusSeeOther)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	events := s.listEvents(ctx)
	s.render(w, "events.html", map[string]interface{}{
		"Events": events,
		"Title":  "Events",
	})
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

func (s *Server) updateApproval(ctx context.Context, name string, phase corev1alpha1.ApprovalRequestPhase, decidedBy, reason string) error {
	approval := &corev1alpha1.ApprovalRequest{}
	ns := s.config.Namespace
	if ns == "" {
		ns = "agents"
	}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, approval); err != nil {
		return err
	}

	now := time.Now()
	approval.Status.Phase = phase
	approval.Status.DecidedBy = decidedBy
	decidedAt := metav1.NewTime(now)
	approval.Status.DecidedAt = &decidedAt
	approval.Status.Reason = reason

	return s.client.Status().Update(ctx, approval)
}

func approvalActorFromContext(ctx context.Context) string {
	user := UserFromContext(ctx)
	if user == nil {
		return "dashboard-user"
	}

	if user.Email != "" {
		return user.Email
	}
	if user.PreferredUser != "" {
		return user.PreferredUser
	}
	if user.Subject != "" {
		return user.Subject
	}

	return "dashboard-user"
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
