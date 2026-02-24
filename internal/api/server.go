/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/api/auth"
	"github.com/marcus-qen/legator/internal/api/rbac"
	"github.com/marcus-qen/legator/internal/inventory"
)

// InventoryProvider provides managed devices for the inventory endpoint.
type InventoryProvider interface {
	Devices() []inventory.ManagedDevice
}

// InventoryStatusProvider optionally exposes sync/status metadata for inventory sources.
type InventoryStatusProvider interface {
	InventoryStatus() map[string]any
}

// ServerConfig configures the Legator API server.
type ServerConfig struct {
	// ListenAddr is the address to listen on (e.g., ":8090").
	ListenAddr string

	// OIDCConfig for JWT validation.
	OIDC auth.OIDCConfig

	// Policies for RBAC evaluation.
	Policies []rbac.UserPolicy

	// Inventory is an optional real-time device inventory source (e.g., Headscale).
	Inventory InventoryProvider

	// UserRateLimit configures per-user request throttling middleware.
	UserRateLimit UserRateLimitConfig
}

// Server is the Legator API server.
type Server struct {
	config    ServerConfig
	k8s       client.Client
	validator *auth.Validator
	rbacEng   *rbac.Engine
	inventory InventoryProvider
	log        logr.Logger
	mux        *http.ServeMux
	userLimiter *userRateLimiter
}

// NewServer creates a new API server.
func NewServer(cfg ServerConfig, k8s client.Client, log logr.Logger) *Server {
	limiterCfg := normalizeUserRateLimitConfig(cfg.UserRateLimit)
	limiterCfg.BypassPaths = mergeBypassPaths(limiterCfg.BypassPaths, cfg.OIDC.BypassPaths)

	s := &Server{
		config:      cfg,
		k8s:         k8s,
		validator:   auth.NewValidator(cfg.OIDC, log.WithName("auth")),
		rbacEng:     rbac.NewEngine(cfg.Policies),
		inventory:   cfg.Inventory,
		log:         log.WithName("api"),
		mux:         http.NewServeMux(),
		userLimiter: newUserRateLimiter(limiterCfg),
	}

	s.registerRoutes()
	return s
}

// Handler returns the HTTP handler with auth middleware applied.
func (s *Server) Handler() http.Handler {
	// Chain: auth -> user rate limit -> handlers, wrapped by audit logging.
	h := http.Handler(s.mux)
	if s.userLimiter != nil {
		h = s.userLimiter.middleware(h, s.effectiveRole)
	}
	h = s.validator.Middleware(h)
	return s.auditMiddleware(h)
}

// Start starts the API server and blocks until context cancellation or server error.
// Implements controller-runtime's manager.Runnable interface for lifecycle management.
func (s *Server) Start(ctx context.Context) error {
	s.log.Info("Starting Legator API server", "addr", s.config.ListenAddr)

	httpSrv := &http.Server{
		Addr:              s.config.ListenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("api shutdown failed: %w", err)
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("api server error after shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("api server failed: %w", err)
		}
		return nil
	}
}

func (s *Server) registerRoutes() {
	// Health (bypasses auth)
	s.mux.HandleFunc("/healthz", s.handleHealthz)

	// Identity
	s.mux.HandleFunc("GET /api/v1/me", s.handleWhoAmI)

	// Agents
	s.mux.HandleFunc("GET /api/v1/agents", s.handleListAgents)
	s.mux.HandleFunc("GET /api/v1/agents/{name}", s.handleGetAgent)
	s.mux.HandleFunc("POST /api/v1/agents/{name}/run", s.handleRunAgent)

	// Runs
	s.mux.HandleFunc("GET /api/v1/runs", s.handleListRuns)
	s.mux.HandleFunc("GET /api/v1/runs/{id}", s.handleGetRun)

	// Inventory
	s.mux.HandleFunc("GET /api/v1/inventory", s.handleListInventory)

	// Approvals
	s.mux.HandleFunc("GET /api/v1/approvals", s.handleListApprovals)
	s.mux.HandleFunc("POST /api/v1/approvals/{id}", s.handleDecideApproval)

	// Audit
	s.mux.HandleFunc("GET /api/v1/audit", s.handleAuditTrail)
}

// --- Handlers ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "no authenticated user")
		return
	}

	type permission struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
	}

	actions := []rbac.Action{
		rbac.ActionViewAgents,
		rbac.ActionViewRuns,
		rbac.ActionViewInventory,
		rbac.ActionViewAudit,
		rbac.ActionRunAgent,
		rbac.ActionApprove,
		rbac.ActionManageDevice,
		rbac.ActionConfigure,
		rbac.ActionChat,
	}

	perms := make(map[string]permission, len(actions))
	for _, action := range actions {
		d := s.rbacEng.Authorize(r.Context(), user, action, "")
		perms[string(action)] = permission{Allowed: d.Allowed, Reason: d.Reason}
	}

	effectiveRole := string(s.effectiveRole(user))

	writeJSON(w, http.StatusOK, map[string]any{
		"subject":       user.Subject,
		"email":         user.Email,
		"name":          user.Name,
		"groups":        user.Groups,
		"effectiveRole": effectiveRole,
		"permissions":   perms,
	})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionViewAgents, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	agents := &corev1alpha1.LegatorAgentList{}
	if err := s.k8s.List(r.Context(), agents); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents: "+err.Error())
		return
	}

	// Sort by name for stable output
	sort.Slice(agents.Items, func(i, j int) bool {
		return agents.Items[i].Name < agents.Items[j].Name
	})

	type agentSummary struct {
		Name       string `json:"name"`
		Namespace  string `json:"namespace"`
		Phase      string `json:"phase"`
		Autonomy   string `json:"autonomy"`
		Schedule   string `json:"schedule"`
		ModelTier  string `json:"modelTier"`
		Paused     bool   `json:"paused"`
		EnvRef     string `json:"environmentRef"`
	}

	result := make([]agentSummary, 0, len(agents.Items))
	for _, a := range agents.Items {
		result = append(result, agentSummary{
			Name:      a.Name,
			Namespace: a.Namespace,
			Phase:     string(a.Status.Phase),
			Autonomy:  string(a.Spec.Guardrails.Autonomy),
			Schedule:  a.Spec.Schedule.Cron,
			ModelTier: string(a.Spec.Model.Tier),
			Paused:    a.Spec.Paused,
			EnvRef:    a.Spec.EnvironmentRef,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agents": result,
		"total":  len(result),
	})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionViewAgents, name); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	agent := &corev1alpha1.LegatorAgent{}
	if err := s.k8s.Get(r.Context(), client.ObjectKey{Name: name, Namespace: "agents"}, agent); err != nil {
		writeError(w, http.StatusNotFound, "agent not found: "+name)
		return
	}

	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleRunAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionRunAgent, name); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	// Parse request body
	var req struct {
		Task     string `json:"task"`
		Target   string `json:"target,omitempty"`
		Autonomy string `json:"autonomy,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Set the run-now annotation to trigger a run
	agent := &corev1alpha1.LegatorAgent{}
	if err := s.k8s.Get(r.Context(), client.ObjectKey{Name: name, Namespace: "agents"}, agent); err != nil {
		writeError(w, http.StatusNotFound, "agent not found: "+name)
		return
	}

	annotations := agent.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["legator.io/run-now"] = "true"
	if req.Task != "" {
		annotations["legator.io/task"] = req.Task
	}
	if req.Target != "" {
		annotations["legator.io/target"] = req.Target
	}
	agent.SetAnnotations(annotations)

	if err := s.k8s.Update(r.Context(), agent); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to trigger run: "+err.Error())
		return
	}

	s.log.Info("Ad-hoc run triggered",
		"agent", name,
		"user", user.Email,
		"task", req.Task,
		"target", req.Target,
	)

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "accepted",
		"agent":  name,
		"task":   req.Task,
	})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionViewRuns, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	runs := &corev1alpha1.LegatorRunList{}
	opts := []client.ListOption{}

	// Optional agent filter
	if agent := r.URL.Query().Get("agent"); agent != "" {
		opts = append(opts, client.MatchingLabels{"legator.io/agent": agent})
	}

	if err := s.k8s.List(r.Context(), runs, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs: "+err.Error())
		return
	}

	// Sort by creation time descending
	sort.Slice(runs.Items, func(i, j int) bool {
		return runs.Items[i].CreationTimestamp.After(runs.Items[j].CreationTimestamp.Time)
	})

	// Limit to 50 most recent
	limit := 50
	if len(runs.Items) > limit {
		runs.Items = runs.Items[:limit]
	}

	type runSummary struct {
		Name      string `json:"name"`
		Agent     string `json:"agent"`
		Phase     string `json:"phase"`
		Trigger   string `json:"trigger"`
		CreatedAt string `json:"createdAt"`
		Duration  string `json:"duration,omitempty"`
	}

	result := make([]runSummary, 0, len(runs.Items))
	for _, run := range runs.Items {
		summary := runSummary{
			Name:      run.Name,
			Agent:     run.Spec.AgentRef,
			Phase:     string(run.Status.Phase),
			Trigger:   string(run.Spec.Trigger),
			CreatedAt: run.CreationTimestamp.Format(time.RFC3339),
		}
		if run.Status.CompletionTime != nil {
			d := run.Status.CompletionTime.Sub(run.CreationTimestamp.Time)
			summary.Duration = d.Round(time.Second).String()
		}
		result = append(result, summary)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"runs":  result,
		"total": len(result),
	})
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionViewRuns, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	run := &corev1alpha1.LegatorRun{}
	if err := s.k8s.Get(r.Context(), client.ObjectKey{Name: id, Namespace: "agents"}, run); err != nil {
		writeError(w, http.StatusNotFound, "run not found: "+id)
		return
	}

	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListInventory(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionViewInventory, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	// Preferred source: live inventory provider (Headscale sync loop, etc.).
	if s.inventory != nil {
		devices := s.inventory.Devices()
		resp := map[string]any{
			"devices": devices,
			"total":   len(devices),
			"source":  "inventory-provider",
		}
		if statusProvider, ok := s.inventory.(InventoryStatusProvider); ok {
			resp["sync"] = statusProvider.InventoryStatus()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Fallback source: LegatorEnvironment endpoints.
	envs := &corev1alpha1.LegatorEnvironmentList{}
	if err := s.k8s.List(r.Context(), envs); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list environments: "+err.Error())
		return
	}

	type endpoint struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		EnvRef string `json:"environmentRef"`
	}

	var endpoints []endpoint
	for _, env := range envs.Items {
		for name, ep := range env.Spec.Endpoints {
			endpoints = append(endpoints, endpoint{
				Name:   name,
				URL:    ep.URL,
				EnvRef: env.Name,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"devices": endpoints,
		"total":   len(endpoints),
		"source":  "environment-endpoints",
	})
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionApprove, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	// List ApprovalRequests with status Pending
	approvals := &corev1alpha1.ApprovalRequestList{}
	if err := s.k8s.List(r.Context(), approvals); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list approvals: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"approvals": approvals.Items,
		"total":     len(approvals.Items),
	})
}

func (s *Server) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionApprove, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	var req struct {
		Decision string `json:"decision"` // "approve" or "deny"
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.Decision != "approve" && req.Decision != "deny" {
		writeError(w, http.StatusBadRequest, "decision must be 'approve' or 'deny'")
		return
	}

	// Get the approval request
	approval := &corev1alpha1.ApprovalRequest{}
	if err := s.k8s.Get(r.Context(), client.ObjectKey{Name: id, Namespace: "agents"}, approval); err != nil {
		writeError(w, http.StatusNotFound, "approval not found: "+id)
		return
	}

	// Update status
	if req.Decision == "approve" {
		approval.Status.Phase = corev1alpha1.ApprovalPhaseApproved
	} else {
		approval.Status.Phase = corev1alpha1.ApprovalPhaseDenied
	}
	approval.Status.DecidedBy = user.Email
	approval.Status.Reason = req.Reason

	if err := s.k8s.Status().Update(r.Context(), approval); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update approval: "+err.Error())
		return
	}

	s.log.Info("Approval decision",
		"id", id,
		"decision", req.Decision,
		"user", user.Email,
		"reason", req.Reason,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   req.Decision + "d",
		"id":       id,
		"decidedBy": user.Email,
	})
}

func (s *Server) handleAuditTrail(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.rbacEng.Authorize(r.Context(), user, rbac.ActionViewAudit, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	// Query runs as audit trail
	runs := &corev1alpha1.LegatorRunList{}
	opts := []client.ListOption{}

	if agent := r.URL.Query().Get("agent"); agent != "" {
		opts = append(opts, client.MatchingLabels{"legator.io/agent": agent})
	}

	if err := s.k8s.List(r.Context(), runs, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query audit trail: "+err.Error())
		return
	}

	// Filter by time if 'since' parameter provided
	since := r.URL.Query().Get("since")
	if since != "" {
		d, err := time.ParseDuration(since)
		if err == nil {
			cutoff := time.Now().Add(-d)
			var filtered []corev1alpha1.LegatorRun
			for _, run := range runs.Items {
				if run.CreationTimestamp.After(cutoff) {
					filtered = append(filtered, run)
				}
			}
			runs.Items = filtered
		}
	}

	// Sort descending
	sort.Slice(runs.Items, func(i, j int) bool {
		return runs.Items[i].CreationTimestamp.After(runs.Items[j].CreationTimestamp.Time)
	})

	type auditEntry struct {
		Run       string `json:"run"`
		Agent     string `json:"agent"`
		Phase     string `json:"phase"`
		Trigger   string `json:"trigger"`
		Time      string `json:"time"`
		Actions   int    `json:"actions"`
		Report    string `json:"report,omitempty"`
	}

	entries := make([]auditEntry, 0, len(runs.Items))
	for _, run := range runs.Items {
		entry := auditEntry{
			Run:     run.Name,
			Agent:   run.Spec.AgentRef,
			Phase:   string(run.Status.Phase),
			Trigger: string(run.Spec.Trigger),
			Time:    run.CreationTimestamp.Format(time.RFC3339),
			Actions: len(run.Status.Actions),
		}
		// Truncate report for list view
		if len(run.Status.Report) > 200 {
			entry.Report = run.Status.Report[:200] + "..."
		} else {
			entry.Report = run.Status.Report
		}
		entries = append(entries, entry)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"total":   len(entries),
	})
}

// --- Middleware ---

func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		rw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		// Log the request
		user := auth.UserFromContext(r.Context())
		email := "anonymous"
		if user != nil {
			email = user.Email
		}

		// Only log non-health endpoints
		if !strings.HasPrefix(r.URL.Path, "/healthz") {
			s.log.Info("API request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.statusCode,
				"user", email,
				"duration", time.Since(start).Round(time.Millisecond).String(),
			)
		}
	})
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) effectiveRole(user *rbac.UserIdentity) rbac.Role {
	if user == nil {
		return rbac.RoleViewer
	}
	if d := s.rbacEng.Authorize(context.Background(), user, rbac.ActionConfigure, ""); d.Allowed {
		return rbac.RoleAdmin
	}
	if d := s.rbacEng.Authorize(context.Background(), user, rbac.ActionRunAgent, ""); d.Allowed {
		return rbac.RoleOperator
	}
	if d := s.rbacEng.Authorize(context.Background(), user, rbac.ActionApprove, ""); d.Allowed {
		return rbac.RoleOperator
	}
	return rbac.RoleViewer
}

func mergeBypassPaths(a, b []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(a)+len(b))
	for _, p := range append(a, b...) {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeForbidden(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":  "forbidden",
		"reason": reason,
	})
}

// PathValue compatibility for Go < 1.22 (unused if using 1.22+).
// Go 1.22+ has r.PathValue() built-in for http.ServeMux patterns.
func init() {
	_ = fmt.Sprintf // avoid unused import
}
