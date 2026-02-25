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
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	"github.com/marcus-qen/legator/internal/api/auth"
	"github.com/marcus-qen/legator/internal/api/rbac"
	"github.com/marcus-qen/legator/internal/approval"
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
	config      ServerConfig
	k8s         client.Client
	validator   *auth.Validator
	rbacEng     *rbac.Engine
	inventory   InventoryProvider
	log         logr.Logger
	mux         *http.ServeMux
	userLimiter *userRateLimiter

	userPolicyCacheMu    sync.RWMutex
	cachedUserPolicies   []rbac.UserPolicy
	userPolicyCacheUntil time.Time
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
	s.mux.HandleFunc("GET /api/v1/cockpit/connectivity", s.handleCockpitConnectivity)

	// Inventory
	s.mux.HandleFunc("GET /api/v1/inventory", s.handleListInventory)

	// Approvals
	s.mux.HandleFunc("GET /api/v1/approvals", s.handleListApprovals)
	s.mux.HandleFunc("POST /api/v1/approvals/{id}", s.handleDecideApproval)

	// Audit
	s.mux.HandleFunc("GET /api/v1/audit", s.handleAuditTrail)
	s.mux.HandleFunc("GET /api/v1/anomalies", s.handleListAnomalies)
	s.mux.HandleFunc("POST /api/v1/policy/simulate", s.handlePolicySimulation)
}

func (s *Server) authorize(ctx context.Context, user *rbac.UserIdentity, action rbac.Action, resource string) rbac.Decision {
	baseDecision := s.rbacEng.Authorize(ctx, user, action, resource)
	if !baseDecision.Allowed || s.k8s == nil || user == nil {
		return baseDecision
	}

	basePolicy, ok := s.rbacEng.ResolvePolicy(user)
	if !ok {
		return baseDecision
	}

	dynamicPolicies, err := s.loadDynamicUserPolicies(ctx)
	if err != nil {
		s.log.Error(err, "Failed to evaluate UserPolicy overlays")
		return rbac.Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("authorization denied: failed to evaluate UserPolicy overlays: %v", err),
		}
	}
	if len(dynamicPolicies) == 0 {
		return baseDecision
	}

	overlayEngine := rbac.NewEngine(dynamicPolicies)
	overlayPolicy, ok := overlayEngine.ResolvePolicy(user)
	if !ok {
		return baseDecision
	}

	return rbac.ComposeDecision(baseDecision, basePolicy, overlayPolicy, action, resource)
}

func (s *Server) loadDynamicUserPolicies(ctx context.Context) ([]rbac.UserPolicy, error) {
	const cacheTTL = 10 * time.Second

	now := time.Now()
	s.userPolicyCacheMu.RLock()
	if now.Before(s.userPolicyCacheUntil) {
		cached := append([]rbac.UserPolicy(nil), s.cachedUserPolicies...)
		s.userPolicyCacheMu.RUnlock()
		return cached, nil
	}
	s.userPolicyCacheMu.RUnlock()

	list := &corev1alpha1.UserPolicyList{}
	if err := s.k8s.List(ctx, list); err != nil {
		if apimeta.IsNoMatchError(err) || strings.Contains(strings.ToLower(err.Error()), "no matches for kind \"userpolicy\"") {
			s.log.Info("UserPolicy CRD not detected; skipping dynamic UserPolicy overlays")
			return nil, nil
		}
		return nil, err
	}

	policies := make([]rbac.UserPolicy, 0, len(list.Items))
	for _, up := range list.Items {
		policies = append(policies, convertUserPolicy(up))
	}

	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Name < policies[j].Name
	})

	s.userPolicyCacheMu.Lock()
	s.cachedUserPolicies = append([]rbac.UserPolicy(nil), policies...)
	s.userPolicyCacheUntil = now.Add(cacheTTL)
	s.userPolicyCacheMu.Unlock()

	return policies, nil
}

func convertUserPolicy(up corev1alpha1.UserPolicy) rbac.UserPolicy {
	subjects := make([]rbac.SubjectMatcher, 0, len(up.Spec.Subjects))
	for _, sub := range up.Spec.Subjects {
		subjects = append(subjects, rbac.SubjectMatcher{
			Claim: sub.Claim,
			Value: sub.Value,
		})
	}

	return rbac.UserPolicy{
		Name:     up.Name,
		Subjects: subjects,
		Role:     rbac.Role(up.Spec.Role),
		Scope: rbac.PolicyScope{
			Tags:        append([]string(nil), up.Spec.Scope.Tags...),
			Namespaces:  append([]string(nil), up.Spec.Scope.Namespaces...),
			Agents:      append([]string(nil), up.Spec.Scope.Agents...),
			MaxAutonomy: rbac.MaxAutonomy(up.Spec.Scope.MaxAutonomy),
		},
	}
}

var policySimulationActions = map[string]rbac.Action{
	string(rbac.ActionViewAgents):    rbac.ActionViewAgents,
	string(rbac.ActionViewRuns):      rbac.ActionViewRuns,
	string(rbac.ActionViewInventory): rbac.ActionViewInventory,
	string(rbac.ActionViewAudit):     rbac.ActionViewAudit,
	string(rbac.ActionRunAgent):      rbac.ActionRunAgent,
	string(rbac.ActionAbortRun):      rbac.ActionAbortRun,
	string(rbac.ActionApprove):       rbac.ActionApprove,
	string(rbac.ActionManageDevice):  rbac.ActionManageDevice,
	string(rbac.ActionConfigure):     rbac.ActionConfigure,
	string(rbac.ActionChat):          rbac.ActionChat,
}

type policySimulationRequest struct {
	Subject            *policySimulationSubject `json:"subject,omitempty"`
	Actions            []string                 `json:"actions,omitempty"`
	Resources          []string                 `json:"resources,omitempty"`
	ProposedPolicy     *policySimulationPolicy  `json:"proposedPolicy,omitempty"`
	RequestRatePerHour int                      `json:"requestRatePerHour,omitempty"`
	RunRatePerHour     int                      `json:"runRatePerHour,omitempty"`
}

type policySimulationSubject struct {
	Subject string            `json:"subject,omitempty"`
	Email   string            `json:"email,omitempty"`
	Name    string            `json:"name,omitempty"`
	Groups  []string          `json:"groups,omitempty"`
	Claims  map[string]string `json:"claims,omitempty"`
}

type policySimulationPolicy struct {
	Name     string                    `json:"name,omitempty"`
	Role     string                    `json:"role"`
	Subjects []policySimulationMatcher `json:"subjects,omitempty"`
	Scope    policySimulationScope     `json:"scope,omitempty"`
}

type policySimulationMatcher struct {
	Claim string `json:"claim"`
	Value string `json:"value"`
}

type policySimulationScope struct {
	Tags        []string `json:"tags,omitempty"`
	Namespaces  []string `json:"namespaces,omitempty"`
	Agents      []string `json:"agents,omitempty"`
	MaxAutonomy string   `json:"maxAutonomy,omitempty"`
}

type projectedRateLimit struct {
	Allowed      bool   `json:"allowed"`
	Reason       string `json:"reason"`
	LimitPerHour int    `json:"limitPerHour"`
	Requested    int    `json:"requestedPerHour"`
}

type simulationDecisionView struct {
	Allowed   bool                `json:"allowed"`
	Reason    string              `json:"reason"`
	RateLimit *projectedRateLimit `json:"rateLimit,omitempty"`
}

type policySimulationEvaluation struct {
	Action    string                 `json:"action"`
	Resource  string                 `json:"resource,omitempty"`
	Current   simulationDecisionView `json:"current"`
	Projected simulationDecisionView `json:"projected"`
}

type policySimulationResponse struct {
	Subject     policySimulationSubject      `json:"subject"`
	BasePolicy  *rbac.UserPolicy             `json:"basePolicy,omitempty"`
	Current     *rbac.UserPolicy             `json:"currentUserPolicy,omitempty"`
	Proposed    *rbac.UserPolicy             `json:"proposedUserPolicy,omitempty"`
	Evaluations []policySimulationEvaluation `json:"evaluations"`
}

func parsePolicySimulationActions(raw []string) ([]rbac.Action, error) {
	if len(raw) == 0 {
		actions := make([]rbac.Action, 0, len(policySimulationActions))
		for _, action := range policySimulationActions {
			actions = append(actions, action)
		}
		slices.Sort(actions)
		return actions, nil
	}

	out := make([]rbac.Action, 0, len(raw))
	for _, item := range raw {
		action, ok := policySimulationActions[item]
		if !ok {
			return nil, fmt.Errorf("unsupported action %q", item)
		}
		out = append(out, action)
	}
	return out, nil
}

func parsePolicySimulationResources(raw []string) []string {
	if len(raw) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func policySubjectFromIdentity(user *rbac.UserIdentity) policySimulationSubject {
	subject := policySimulationSubject{
		Subject: user.Subject,
		Email:   user.Email,
		Name:    user.Name,
		Groups:  append([]string(nil), user.Groups...),
	}
	return subject
}

func roleClamp(base rbac.Role, overlay rbac.Role) rbac.Role {
	if roleRank(overlay) < roleRank(base) {
		return overlay
	}
	return base
}

func roleRank(role rbac.Role) int {
	switch role {
	case rbac.RoleViewer:
		return 1
	case rbac.RoleOperator:
		return 2
	case rbac.RoleAdmin:
		return 3
	default:
		return 0
	}
}

func projectRateLimit(role rbac.Role, action rbac.Action, requestRatePerHour, runRatePerHour int) *projectedRateLimit {
	requestLimit := 600
	runLimit := 30
	switch role {
	case rbac.RoleOperator:
		requestLimit = 1200
		runLimit = 120
	case rbac.RoleAdmin:
		requestLimit = 2400
		runLimit = 240
	}

	isRunLike := action == rbac.ActionRunAgent || action == rbac.ActionApprove ||
		action == rbac.ActionConfigure || action == rbac.ActionManageDevice || action == rbac.ActionAbortRun
	if isRunLike {
		requested := runRatePerHour
		if requested <= 0 {
			requested = 1
		}
		if requested > runLimit {
			return &projectedRateLimit{
				Allowed:      false,
				Reason:       fmt.Sprintf("projected run rate %d/h exceeds %d/h limit for role %s", requested, runLimit, role),
				LimitPerHour: runLimit,
				Requested:    requested,
			}
		}
		return &projectedRateLimit{
			Allowed:      true,
			Reason:       fmt.Sprintf("projected run rate %d/h within %d/h limit for role %s", requested, runLimit, role),
			LimitPerHour: runLimit,
			Requested:    requested,
		}
	}

	requested := requestRatePerHour
	if requested <= 0 {
		requested = 1
	}
	if requested > requestLimit {
		return &projectedRateLimit{
			Allowed:      false,
			Reason:       fmt.Sprintf("projected request rate %d/h exceeds %d/h limit for role %s", requested, requestLimit, role),
			LimitPerHour: requestLimit,
			Requested:    requested,
		}
	}
	return &projectedRateLimit{
		Allowed:      true,
		Reason:       fmt.Sprintf("projected request rate %d/h within %d/h limit for role %s", requested, requestLimit, role),
		LimitPerHour: requestLimit,
		Requested:    requested,
	}
}

func convertSimulationPolicy(policy *policySimulationPolicy) (*rbac.UserPolicy, error) {
	if policy == nil {
		return nil, nil
	}

	role := rbac.Role(strings.TrimSpace(policy.Role))
	switch role {
	case rbac.RoleViewer, rbac.RoleOperator, rbac.RoleAdmin:
	default:
		return nil, fmt.Errorf("invalid proposed role %q", policy.Role)
	}

	subjects := make([]rbac.SubjectMatcher, 0, len(policy.Subjects))
	for _, subject := range policy.Subjects {
		if strings.TrimSpace(subject.Claim) == "" || strings.TrimSpace(subject.Value) == "" {
			return nil, fmt.Errorf("proposed policy subjects must include claim and value")
		}
		subjects = append(subjects, rbac.SubjectMatcher{Claim: subject.Claim, Value: subject.Value})
	}
	if len(subjects) == 0 {
		return nil, fmt.Errorf("proposed policy requires at least one subject matcher")
	}

	name := strings.TrimSpace(policy.Name)
	if name == "" {
		name = "proposed-policy"
	}

	return &rbac.UserPolicy{
		Name:     name,
		Subjects: subjects,
		Role:     role,
		Scope: rbac.PolicyScope{
			Tags:        append([]string(nil), policy.Scope.Tags...),
			Namespaces:  append([]string(nil), policy.Scope.Namespaces...),
			Agents:      append([]string(nil), policy.Scope.Agents...),
			MaxAutonomy: rbac.MaxAutonomy(policy.Scope.MaxAutonomy),
		},
	}, nil
}

func simulationIdentity(caller *rbac.UserIdentity, req policySimulationRequest) *rbac.UserIdentity {
	if req.Subject == nil {
		return caller
	}
	user := &rbac.UserIdentity{
		Subject: req.Subject.Subject,
		Email:   req.Subject.Email,
		Name:    req.Subject.Name,
		Groups:  append([]string(nil), req.Subject.Groups...),
		Claims:  map[string]any{},
	}
	for k, v := range req.Subject.Claims {
		user.Claims[k] = v
	}
	if user.Claims == nil {
		user.Claims = map[string]any{}
	}
	if user.Claims["email"] == nil && user.Email != "" {
		user.Claims["email"] = user.Email
	}
	if user.Claims["sub"] == nil && user.Subject != "" {
		user.Claims["sub"] = user.Subject
	}
	if user.Claims["name"] == nil && user.Name != "" {
		user.Claims["name"] = user.Name
	}
	if user.Claims["groups"] == nil && len(user.Groups) > 0 {
		arr := make([]any, 0, len(user.Groups))
		for _, group := range user.Groups {
			arr = append(arr, group)
		}
		user.Claims["groups"] = arr
	}
	return user
}

func sameIdentity(a, b *rbac.UserIdentity) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Email != b.Email || a.Subject != b.Subject || a.Name != b.Name {
		return false
	}
	if len(a.Groups) != len(b.Groups) {
		return false
	}
	for i := range a.Groups {
		if a.Groups[i] != b.Groups[i] {
			return false
		}
	}
	return true
}

func effectiveRole(base *rbac.UserPolicy, overlay *rbac.UserPolicy) rbac.Role {
	role := rbac.RoleViewer
	if base != nil {
		role = base.Role
	}
	if overlay != nil {
		role = roleClamp(role, overlay.Role)
	}
	return role
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
		d := s.authorize(r.Context(), user, action, "")
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
	if d := s.authorize(r.Context(), user, rbac.ActionViewAgents, ""); !d.Allowed {
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
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Phase     string `json:"phase"`
		Autonomy  string `json:"autonomy"`
		Schedule  string `json:"schedule"`
		ModelTier string `json:"modelTier"`
		Paused    bool   `json:"paused"`
		EnvRef    string `json:"environmentRef"`
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
	if d := s.authorize(r.Context(), user, rbac.ActionViewAgents, name); !d.Allowed {
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
	if d := s.authorize(r.Context(), user, rbac.ActionRunAgent, name); !d.Allowed {
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
	if d := s.authorize(r.Context(), user, rbac.ActionViewRuns, ""); !d.Allowed {
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
	if d := s.authorize(r.Context(), user, rbac.ActionViewRuns, ""); !d.Allowed {
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

type cockpitTunnelSnapshot struct {
	Status           string `json:"status"`
	Provider         string `json:"provider"`
	ControlServer    string `json:"controlServer,omitempty"`
	RouteID          string `json:"routeId"`
	Target           string `json:"target,omitempty"`
	AllowedPorts     []int  `json:"allowedPorts,omitempty"`
	LeaseTTLSeconds  int64  `json:"leaseTtlSeconds,omitempty"`
	StartedAt        string `json:"startedAt,omitempty"`
	ExpiresAt        string `json:"expiresAt,omitempty"`
	LastTransitionAt string `json:"lastTransitionAt,omitempty"`
}

type cockpitCredentialSnapshot struct {
	Mode       string `json:"mode"`
	Issuer     string `json:"issuer,omitempty"`
	TTLSeconds int64  `json:"ttlSeconds,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
}

type cockpitConnectivitySnapshot struct {
	Run         string                    `json:"run"`
	Agent       string                    `json:"agent"`
	Environment string                    `json:"environment"`
	Phase       string                    `json:"phase"`
	Tunnel      cockpitTunnelSnapshot     `json:"tunnel"`
	Credential  cockpitCredentialSnapshot `json:"credential"`
}

func (s *Server) handleCockpitConnectivity(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.authorize(r.Context(), user, rbac.ActionViewRuns, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	runs := &corev1alpha1.LegatorRunList{}
	listOpts := []client.ListOption{}
	if agent := strings.TrimSpace(r.URL.Query().Get("agent")); agent != "" {
		listOpts = append(listOpts, client.MatchingLabels{"legator.io/agent": agent})
	}
	if err := s.k8s.List(r.Context(), runs, listOpts...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs: "+err.Error())
		return
	}

	sort.Slice(runs.Items, func(i, j int) bool {
		return runs.Items[i].CreationTimestamp.After(runs.Items[j].CreationTimestamp.Time)
	})

	limit := 20
	if q := strings.TrimSpace(r.URL.Query().Get("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			switch {
			case n <= 0:
				limit = 20
			case n > 100:
				limit = 100
			default:
				limit = n
			}
		}
	}
	if len(runs.Items) > limit {
		runs.Items = runs.Items[:limit]
	}

	envCache := map[string]*corev1alpha1.LegatorEnvironment{}
	now := time.Now().UTC()
	result := make([]cockpitConnectivitySnapshot, 0, len(runs.Items))

	for _, run := range runs.Items {
		envName := strings.TrimSpace(run.Spec.EnvironmentRef)
		var env *corev1alpha1.LegatorEnvironment
		if envName != "" {
			if cached, ok := envCache[envName]; ok {
				env = cached
			} else {
				candidate := &corev1alpha1.LegatorEnvironment{}
				if err := s.k8s.Get(r.Context(), client.ObjectKey{Name: envName, Namespace: "agents"}, candidate); err == nil {
					env = candidate
					envCache[envName] = candidate
				} else {
					envCache[envName] = nil
				}
			}
		}

		result = append(result, buildCockpitConnectivitySnapshot(run, env, now))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"runs":        result,
		"total":       len(result),
		"generatedAt": now.Format(time.RFC3339),
	})
}

func buildCockpitConnectivitySnapshot(run corev1alpha1.LegatorRun, env *corev1alpha1.LegatorEnvironment, now time.Time) cockpitConnectivitySnapshot {
	startedAt := run.CreationTimestamp.Time.UTC()
	if run.Status.StartTime != nil {
		startedAt = run.Status.StartTime.Time.UTC()
	}

	provider := "direct"
	controlServer := ""
	allowedPorts := []int{}
	leaseTTL := int64(0)
	if env != nil && env.Spec.Connectivity != nil {
		spec := env.Spec.Connectivity
		if strings.TrimSpace(spec.Type) != "" {
			provider = strings.TrimSpace(spec.Type)
		}
		if spec.Headscale != nil {
			controlServer = strings.TrimSpace(spec.Headscale.ControlServer)
		}
		allowedPorts = collectAllowedPorts(env.Spec.Endpoints)
		if provider == "headscale" || provider == "tailscale" {
			leaseTTL = int64((15 * time.Minute).Seconds())
		}
	}

	credentialMode, credentialIssuer, credentialTTL := inferCredentialProfile(env)
	credentialExpiresAt := ""
	if credentialTTL > 0 {
		credentialExpiresAt = startedAt.Add(credentialTTL).Format(time.RFC3339)
	}

	tunnelStatus := resolveTunnelStatus(run, startedAt, now)
	tunnelExpiresAt := ""
	if run.Status.CompletionTime != nil {
		tunnelExpiresAt = run.Status.CompletionTime.Time.UTC().Format(time.RFC3339)
	} else if leaseTTL > 0 {
		tunnelExpiresAt = startedAt.Add(time.Duration(leaseTTL) * time.Second).Format(time.RFC3339)
	}

	lastTransition := startedAt
	if run.Status.CompletionTime != nil {
		lastTransition = run.Status.CompletionTime.Time.UTC()
	}

	return cockpitConnectivitySnapshot{
		Run:         run.Name,
		Agent:       run.Spec.AgentRef,
		Environment: run.Spec.EnvironmentRef,
		Phase:       string(run.Status.Phase),
		Tunnel: cockpitTunnelSnapshot{
			Status:           tunnelStatus,
			Provider:         provider,
			ControlServer:    controlServer,
			RouteID:          fmt.Sprintf("run/%s", run.Name),
			Target:           inferRunTarget(run),
			AllowedPorts:     allowedPorts,
			LeaseTTLSeconds:  leaseTTL,
			StartedAt:        startedAt.Format(time.RFC3339),
			ExpiresAt:        tunnelExpiresAt,
			LastTransitionAt: lastTransition.Format(time.RFC3339),
		},
		Credential: cockpitCredentialSnapshot{
			Mode:       credentialMode,
			Issuer:     credentialIssuer,
			TTLSeconds: int64(credentialTTL.Seconds()),
			ExpiresAt:  credentialExpiresAt,
		},
	}
}

func resolveTunnelStatus(run corev1alpha1.LegatorRun, startedAt, now time.Time) string {
	switch run.Status.Phase {
	case corev1alpha1.RunPhasePending:
		return "requested"
	case corev1alpha1.RunPhaseRunning:
		if now.Sub(startedAt) < 10*time.Second {
			return "establishing"
		}
		return "active"
	case corev1alpha1.RunPhaseFailed:
		return "failed"
	case corev1alpha1.RunPhaseSucceeded, corev1alpha1.RunPhaseBlocked, corev1alpha1.RunPhaseEscalated:
		return "expired"
	default:
		return "requested"
	}
}

func inferRunTarget(run corev1alpha1.LegatorRun) string {
	for i := len(run.Status.Actions) - 1; i >= 0; i-- {
		target := strings.TrimSpace(run.Status.Actions[i].Target)
		if target != "" {
			return target
		}
	}
	return string(run.Spec.Trigger)
}

func collectAllowedPorts(endpoints map[string]corev1alpha1.EndpointSpec) []int {
	if len(endpoints) == 0 {
		return nil
	}
	seen := map[int]struct{}{}
	ports := make([]int, 0, len(endpoints))
	for _, ep := range endpoints {
		if p := endpointPort(ep.URL); p > 0 {
			if _, ok := seen[p]; !ok {
				seen[p] = struct{}{}
				ports = append(ports, p)
			}
		}
	}
	sort.Ints(ports)
	return ports
}

func endpointPort(raw string) int {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return 0
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		return n
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return 80
	case "ssh":
		return 22
	default:
		return 443
	}
}

func inferCredentialProfile(env *corev1alpha1.LegatorEnvironment) (mode, issuer string, ttl time.Duration) {
	mode = "none"
	if env == nil || len(env.Spec.Credentials) == 0 {
		return mode, "", 0
	}

	keys := make([]string, 0, len(env.Spec.Credentials))
	for key := range env.Spec.Credentials {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	fallbackMode := "none"
	fallbackIssuer := ""
	for _, key := range keys {
		cred := env.Spec.Credentials[key]
		credType := strings.TrimSpace(cred.Type)

		switch credType {
		case "vault-ssh-ca":
			mode = "vault-signed-cert"
			issuer = credentialIssuer(cred, key)
			ttl = parseDurationOrDefault(cred.Vault, 5*time.Minute)
			return
		case "vault-kv":
			keyOrPath := strings.ToLower(key + " " + credentialIssuer(cred, key))
			if strings.Contains(keyOrPath, "otp") {
				mode = "otp"
				issuer = credentialIssuer(cred, key)
				ttl = parseDurationOrDefault(cred.Vault, 2*time.Minute)
				return
			}
			if fallbackMode == "none" {
				fallbackMode = "vault-kv"
				fallbackIssuer = credentialIssuer(cred, key)
			}
		default:
			if fallbackMode == "none" {
				fallbackMode = "static-key-legacy"
				fallbackIssuer = credentialIssuer(cred, key)
			}
		}
	}

	if fallbackMode != "none" {
		return fallbackMode, fallbackIssuer, 0
	}
	return mode, "", 0
}

func credentialIssuer(cred corev1alpha1.CredentialRef, name string) string {
	if cred.Vault != nil {
		parts := make([]string, 0, 3)
		if mount := strings.TrimSpace(cred.Vault.Mount); mount != "" {
			parts = append(parts, mount)
		}
		if role := strings.TrimSpace(cred.Vault.Role); role != "" {
			parts = append(parts, "role="+role)
		}
		if path := strings.TrimSpace(cred.Vault.Path); path != "" {
			parts = append(parts, "path="+path)
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	if secret := strings.TrimSpace(cred.SecretRef); secret != "" {
		return "secret/" + secret
	}
	if typ := strings.TrimSpace(cred.Type); typ != "" {
		return typ
	}
	return name
}

func parseDurationOrDefault(v *corev1alpha1.VaultCredentialSpec, def time.Duration) time.Duration {
	if v == nil {
		return def
	}
	if strings.TrimSpace(v.TTL) == "" {
		return def
	}
	d, err := time.ParseDuration(v.TTL)
	if err != nil {
		return def
	}
	if d <= 0 {
		return def
	}
	return d
}

func (s *Server) handleListInventory(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.authorize(r.Context(), user, rbac.ActionViewInventory, ""); !d.Allowed {
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
	if d := s.authorize(r.Context(), user, rbac.ActionApprove, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	// Keep approvals listing bounded to the canonical namespace and with a hard timeout
	// so ChatOps/read-path requests fail fast instead of hanging under API pressure.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	approvals := &corev1alpha1.ApprovalRequestList{}
	if err := s.k8s.List(ctx, approvals, client.InNamespace("agents")); err != nil {
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
	if d := s.authorize(r.Context(), user, rbac.ActionApprove, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	var req struct {
		Decision          string `json:"decision"` // "approve" or "deny"
		Reason            string `json:"reason"`
		TypedConfirmation string `json:"typedConfirmation,omitempty"`
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
	ar := &corev1alpha1.ApprovalRequest{}
	if err := s.k8s.Get(r.Context(), client.ObjectKey{Name: id, Namespace: "agents"}, ar); err != nil {
		writeError(w, http.StatusNotFound, "approval not found: "+id)
		return
	}

	if req.Decision == "approve" {
		if err := approval.ValidateTypedConfirmation(ar, req.TypedConfirmation, time.Now()); err != nil {
			if strings.Contains(err.Error(), "required") {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.Contains(err.Error(), "expired") {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeForbidden(w, err.Error())
			return
		}
	}

	// Update status
	if req.Decision == "approve" {
		ar.Status.Phase = corev1alpha1.ApprovalPhaseApproved
	} else {
		ar.Status.Phase = corev1alpha1.ApprovalPhaseDenied
	}
	ar.Status.DecidedBy = user.Email
	ar.Status.Reason = req.Reason

	if err := s.k8s.Status().Update(r.Context(), ar); err != nil {
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
		"status":    req.Decision + "d",
		"id":        id,
		"decidedBy": user.Email,
	})
}

func (s *Server) handleAuditTrail(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.authorize(r.Context(), user, rbac.ActionViewAudit, ""); !d.Allowed {
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
		Run                string         `json:"run"`
		Agent              string         `json:"agent"`
		Phase              string         `json:"phase"`
		Trigger            string         `json:"trigger"`
		Time               string         `json:"time"`
		Actions            int            `json:"actions"`
		SafetyBlocked      int            `json:"safetyBlocked"`
		ApprovalsRequired  int            `json:"approvalsRequired"`
		ApprovalsApproved  int            `json:"approvalsApproved"`
		ApprovalsDenied    int            `json:"approvalsDenied"`
		SafetyOutcomeStats map[string]int `json:"safetyOutcomeStats,omitempty"`
		Report             string         `json:"report,omitempty"`
	}

	entries := make([]auditEntry, 0, len(runs.Items))
	for _, run := range runs.Items {
		entry := auditEntry{
			Run:                run.Name,
			Agent:              run.Spec.AgentRef,
			Phase:              string(run.Status.Phase),
			Trigger:            string(run.Spec.Trigger),
			Time:               run.CreationTimestamp.Format(time.RFC3339),
			Actions:            len(run.Status.Actions),
			SafetyOutcomeStats: map[string]int{},
		}
		for _, a := range run.Status.Actions {
			if a.Status == corev1alpha1.ActionStatusBlocked || a.Status == corev1alpha1.ActionStatusDenied {
				entry.SafetyBlocked++
			}
			if a.PreFlightCheck == nil {
				continue
			}
			if strings.EqualFold(a.PreFlightCheck.ApprovalCheck, "REQUIRED") {
				entry.ApprovalsRequired++
			}
			switch strings.ToUpper(strings.TrimSpace(a.PreFlightCheck.ApprovalDecision)) {
			case "APPROVED":
				entry.ApprovalsApproved++
			case "DENIED":
				entry.ApprovalsDenied++
			}
			outcome := strings.ToUpper(strings.TrimSpace(a.PreFlightCheck.SafetyGateOutcome))
			if outcome != "" {
				entry.SafetyOutcomeStats[outcome]++
			}
		}
		if len(entry.SafetyOutcomeStats) == 0 {
			entry.SafetyOutcomeStats = nil
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

func (s *Server) handleListAnomalies(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if d := s.authorize(r.Context(), user, rbac.ActionViewAudit, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	events := &corev1alpha1.AgentEventList{}
	if err := s.k8s.List(r.Context(), events); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list anomalies: "+err.Error())
		return
	}

	type anomalyEntry struct {
		ID          string            `json:"id"`
		SourceAgent string            `json:"sourceAgent"`
		SourceRun   string            `json:"sourceRun,omitempty"`
		Severity    string            `json:"severity"`
		Summary     string            `json:"summary"`
		Detail      string            `json:"detail,omitempty"`
		Labels      map[string]string `json:"labels,omitempty"`
		Time        string            `json:"time"`
	}

	entries := make([]anomalyEntry, 0, len(events.Items))
	for _, event := range events.Items {
		if event.Spec.EventType != "anomaly" {
			continue
		}
		entries = append(entries, anomalyEntry{
			ID:          event.Name,
			SourceAgent: event.Spec.SourceAgent,
			SourceRun:   event.Spec.SourceRun,
			Severity:    string(event.Spec.Severity),
			Summary:     event.Spec.Summary,
			Detail:      event.Spec.Detail,
			Labels:      event.Spec.Labels,
			Time:        event.CreationTimestamp.Format(time.RFC3339),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Time > entries[j].Time
	})

	severityCounts := map[string]int{
		"info":     0,
		"warning":  0,
		"critical": 0,
	}
	for _, entry := range entries {
		severityCounts[entry.Severity]++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"anomalies": entries,
		"total":     len(entries),
		"severity":  severityCounts,
	})
}

func (s *Server) handlePolicySimulation(w http.ResponseWriter, r *http.Request) {
	caller := auth.UserFromContext(r.Context())
	if d := s.authorize(r.Context(), caller, rbac.ActionViewAudit, ""); !d.Allowed {
		writeForbidden(w, d.Reason)
		return
	}

	var req policySimulationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	simUser := simulationIdentity(caller, req)
	if simUser == nil {
		writeError(w, http.StatusBadRequest, "simulation subject is required")
		return
	}

	if req.Subject != nil && !sameIdentity(caller, simUser) {
		if d := s.authorize(r.Context(), caller, rbac.ActionConfigure, ""); !d.Allowed {
			writeForbidden(w, "simulating a different subject requires config:write")
			return
		}
	}

	actions, err := parsePolicySimulationActions(req.Actions)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resources := parsePolicySimulationResources(req.Resources)

	basePolicy, _ := s.rbacEng.ResolvePolicy(simUser)

	var currentOverlay *rbac.UserPolicy
	if s.k8s != nil {
		if policies, loadErr := s.loadDynamicUserPolicies(r.Context()); loadErr == nil {
			overlayEngine := rbac.NewEngine(policies)
			if policy, ok := overlayEngine.ResolvePolicy(simUser); ok {
				currentOverlay = policy
			}
		}
	}

	proposedPolicy, err := convertSimulationPolicy(req.ProposedPolicy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var proposedOverlay *rbac.UserPolicy
	if proposedPolicy != nil {
		engine := rbac.NewEngine([]rbac.UserPolicy{*proposedPolicy})
		if policy, ok := engine.ResolvePolicy(simUser); ok {
			proposedOverlay = policy
		}
	}

	currentRole := effectiveRole(basePolicy, currentOverlay)
	projectedRole := effectiveRole(basePolicy, proposedOverlay)

	evaluations := make([]policySimulationEvaluation, 0, len(actions)*len(resources))
	for _, action := range actions {
		for _, resource := range resources {
			currentDecision := s.authorize(r.Context(), simUser, action, resource)

			baseDecision := s.rbacEng.Authorize(r.Context(), simUser, action, resource)
			projectedDecision := baseDecision
			if baseDecision.Allowed && basePolicy != nil && proposedOverlay != nil {
				projectedDecision = rbac.ComposeDecision(baseDecision, basePolicy, proposedOverlay, action, resource)
			}

			currentRate := projectRateLimit(currentRole, action, req.RequestRatePerHour, req.RunRatePerHour)
			projectedRate := projectRateLimit(projectedRole, action, req.RequestRatePerHour, req.RunRatePerHour)
			if !currentDecision.Allowed {
				currentRate = &projectedRateLimit{
					Allowed:      false,
					Reason:       "blocked by authorization decision",
					LimitPerHour: currentRate.LimitPerHour,
					Requested:    currentRate.Requested,
				}
			}
			if !projectedDecision.Allowed {
				projectedRate = &projectedRateLimit{
					Allowed:      false,
					Reason:       "blocked by authorization decision",
					LimitPerHour: projectedRate.LimitPerHour,
					Requested:    projectedRate.Requested,
				}
			}

			evaluations = append(evaluations, policySimulationEvaluation{
				Action:   string(action),
				Resource: resource,
				Current: simulationDecisionView{
					Allowed:   currentDecision.Allowed,
					Reason:    currentDecision.Reason,
					RateLimit: currentRate,
				},
				Projected: simulationDecisionView{
					Allowed:   projectedDecision.Allowed,
					Reason:    projectedDecision.Reason,
					RateLimit: projectedRate,
				},
			})
		}
	}

	writeJSON(w, http.StatusOK, policySimulationResponse{
		Subject:     policySubjectFromIdentity(simUser),
		BasePolicy:  basePolicy,
		Current:     currentOverlay,
		Proposed:    proposedOverlay,
		Evaluations: evaluations,
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
	if d := s.authorize(context.Background(), user, rbac.ActionConfigure, ""); d.Allowed {
		return rbac.RoleAdmin
	}
	if d := s.authorize(context.Background(), user, rbac.ActionRunAgent, ""); d.Allowed {
		return rbac.RoleOperator
	}
	if d := s.authorize(context.Background(), user, rbac.ActionApprove, ""); d.Allowed {
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
