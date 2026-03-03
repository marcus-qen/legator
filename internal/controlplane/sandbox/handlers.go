package sandbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// EventPublisher is the interface for emitting bus events (satisfied by *events.Bus).
type EventPublisher interface {
	Publish(evt BusEvent)
}

// BusEvent is a minimal event representation so this package does not import
// the events package directly (avoiding a circular dependency).
type BusEvent struct {
	Type      string      `json:"type"`
	ProbeID   string      `json:"probe_id,omitempty"`
	Summary   string      `json:"summary"`
	Detail    interface{} `json:"detail,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// AuditRecorder emits audit events (satisfied by the server's audit recorder).
type AuditRecorder interface {
	Emit(eventType, probeID, actor, summary string)
}

// Handler exposes REST endpoints for sandbox session management.
// HandlerPolicy holds admittance rules evaluated at create time.
type HandlerPolicy struct {
	// AllowedRuntimes restricts which runtime_class values may be requested.
	// An empty or nil slice means all runtimes are allowed.
	AllowedRuntimes []string

	// MaxConcurrent caps the number of non-terminal sandbox sessions globally.
	// Zero or negative means unlimited.
	MaxConcurrent int

	// ProbeValidator, if non-nil, is called to verify the probe_id exists.
	// Return false to reject the create request.
	ProbeValidator func(probeID string) bool
}

type Handler struct {
	store  *Store
	events EventPublisher
	audit  AuditRecorder
	logger *zap.Logger
	policy HandlerPolicy
}

// NewHandler creates a Handler wired to the given store, event publisher,
// audit recorder, and logger. Any of the optional interfaces may be nil.
func NewHandler(store *Store, events EventPublisher, audit AuditRecorder, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{
		store:  store,
		events: events,
		audit:  audit,
		logger: logger,
	}
}

// SetPolicy replaces the handler's admittance policy. Safe to call before
// any requests are handled.
func (h *Handler) SetPolicy(p HandlerPolicy) {
	h.policy = p
}

// ── Route helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Response headers already sent; nothing to do.
		return
	}
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Error: msg, Code: code})
}

// workspaceFromRequest returns the workspace_id query param (empty if absent).
// Callers that need isolation should treat "" as "all accessible" or enforce via
// their own workspace middleware.
func workspaceFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("workspace_id"))
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// HandleCreate handles POST /api/v1/sandboxes
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID  string            `json:"workspace_id"`
		ProbeID      string            `json:"probe_id"`
		TemplateID   string            `json:"template_id"`
		RuntimeClass string            `json:"runtime_class"`
		CreatedBy    string            `json:"created_by"`
		TTLSeconds   int64             `json:"ttl_seconds"`
		Metadata     map[string]string `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if strings.TrimSpace(body.RuntimeClass) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "runtime_class is required")
		return
	}

	runtimeClass := strings.TrimSpace(body.RuntimeClass)

	// Policy: allowed runtimes
	if len(h.policy.AllowedRuntimes) > 0 {
		allowed := false
		for _, rt := range h.policy.AllowedRuntimes {
			if strings.EqualFold(rt, runtimeClass) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeJSONError(w, http.StatusForbidden, "runtime_not_allowed",
				fmt.Sprintf("runtime_class %q is not permitted by policy", runtimeClass))
			return
		}
	}

	// Policy: probe validation
	probeID := strings.TrimSpace(body.ProbeID)
	if h.policy.ProbeValidator != nil && probeID != "" {
		if !h.policy.ProbeValidator(probeID) {
			writeJSONError(w, http.StatusUnprocessableEntity, "probe_not_found",
				fmt.Sprintf("probe %q does not exist", probeID))
			return
		}
	}

	// Policy: max concurrent (non-terminal)
	if h.policy.MaxConcurrent > 0 {
		active := h.store.CountActive("")
		if active >= h.policy.MaxConcurrent {
			writeJSONError(w, http.StatusTooManyRequests, "max_concurrent_exceeded",
				fmt.Sprintf("maximum concurrent sandboxes (%d) reached", h.policy.MaxConcurrent))
			return
		}
	}

	sess := &SandboxSession{
		WorkspaceID:  strings.TrimSpace(body.WorkspaceID),
		ProbeID:      strings.TrimSpace(body.ProbeID),
		TemplateID:   strings.TrimSpace(body.TemplateID),
		RuntimeClass: strings.TrimSpace(body.RuntimeClass),
		CreatedBy:    strings.TrimSpace(body.CreatedBy),
		TTL:          time.Duration(body.TTLSeconds) * time.Second,
		Metadata:     body.Metadata,
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]string)
	}

	created, err := h.store.Create(sess)
	if err != nil {
		h.logger.Error("create sandbox session", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to create session")
		return
	}

	h.emitAudit("sandbox.created", created.ProbeID, created.CreatedBy,
		fmt.Sprintf("Sandbox session created: %s", created.ID))
	h.emitEvent("sandbox.created", created.ProbeID,
		fmt.Sprintf("Sandbox session created: %s", created.ID), created)

	writeJSON(w, http.StatusCreated, created)
}

// HandleList handles GET /api/v1/sandboxes
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ListFilter{
		WorkspaceID: strings.TrimSpace(q.Get("workspace_id")),
		State:       strings.TrimSpace(q.Get("state")),
		ProbeID:     strings.TrimSpace(q.Get("probe_id")),
	}

	sessions, err := h.store.List(f)
	if err != nil {
		h.logger.Error("list sandbox sessions", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list sessions")
		return
	}
	if sessions == nil {
		sessions = []*SandboxSession{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": sessions,
		"total":    len(sessions),
	})
}

// HandleGet handles GET /api/v1/sandboxes/{id}
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing session id")
		return
	}

	wsID := workspaceFromRequest(r)
	sess, err := h.store.GetForWorkspace(id, wsID)
	if err != nil {
		h.logger.Error("get sandbox session", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get session")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}

	writeJSON(w, http.StatusOK, sess)
}

// HandleDestroy handles DELETE /api/v1/sandboxes/{id}
// Transitions a non-terminal session to "destroyed".
func (h *Handler) HandleDestroy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing session id")
		return
	}

	wsID := workspaceFromRequest(r)
	existing, err := h.store.GetForWorkspace(id, wsID)
	if err != nil {
		h.logger.Error("destroy sandbox session (get)", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch session")
		return
	}
	if existing == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if existing.IsTerminal() {
		writeJSONError(w, http.StatusConflict, "already_terminal",
			fmt.Sprintf("session is already in terminal state %q", existing.State))
		return
	}

	updated, err := h.store.Transition(id, existing.State, StateDestroyed)
	if err != nil {
		h.logger.Error("destroy sandbox session (transition)", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusConflict, "transition_failed", err.Error())
		return
	}

	h.emitAudit("sandbox.destroyed", updated.ProbeID, "api",
		fmt.Sprintf("Sandbox session destroyed: %s", id))
	h.emitEvent("sandbox.destroyed", updated.ProbeID,
		fmt.Sprintf("Sandbox session destroyed: %s", id), updated)

	writeJSON(w, http.StatusOK, updated)
}

// HandleTransition handles POST /api/v1/sandboxes/{id}/transition
// Body: {"from": "created", "to": "provisioning", "error_message": "optional"}
func (h *Handler) HandleTransition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing session id")
		return
	}

	var body struct {
		From         string `json:"from"`
		To           string `json:"to"`
		ErrorMessage string `json:"error_message"`
		TaskID       string `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	body.From = strings.TrimSpace(body.From)
	body.To = strings.TrimSpace(body.To)
	if body.From == "" || body.To == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "from and to are required")
		return
	}

	wsID := workspaceFromRequest(r)
	existing, err := h.store.GetForWorkspace(id, wsID)
	if err != nil {
		h.logger.Error("transition sandbox session (get)", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch session")
		return
	}
	if existing == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}

	updated, err := h.store.Transition(id, body.From, body.To)
	if err != nil {
		writeJSONError(w, http.StatusConflict, "transition_failed", err.Error())
		return
	}

	// Persist optional side-effect fields.
	if body.ErrorMessage != "" || body.TaskID != "" {
		updated.ErrorMessage = body.ErrorMessage
		updated.TaskID = body.TaskID
		if uErr := h.store.Update(updated); uErr != nil {
			h.logger.Warn("persist sandbox update after transition", zap.Error(uErr))
		}
		// Refresh from store so caller sees canonical state.
		if refreshed, _ := h.store.Get(id); refreshed != nil {
			updated = refreshed
		}
	}

	h.emitAudit("sandbox."+body.To, updated.ProbeID, "api",
		fmt.Sprintf("Sandbox session %s: %s → %s", id, body.From, body.To))
	h.emitEvent("sandbox."+body.To, updated.ProbeID,
		fmt.Sprintf("Sandbox %s → %s", body.From, body.To), updated)

	writeJSON(w, http.StatusOK, updated)
}

// ── Private helpers ───────────────────────────────────────────────────────────

func (h *Handler) emitEvent(typ, probeID, summary string, detail interface{}) {
	if h.events == nil {
		return
	}
	h.events.Publish(BusEvent{
		Type:      typ,
		ProbeID:   probeID,
		Summary:   summary,
		Detail:    detail,
		Timestamp: time.Now().UTC(),
	})
}

func (h *Handler) emitAudit(eventType, probeID, actor, summary string) {
	if h.audit == nil {
		return
	}
	h.audit.Emit(eventType, probeID, actor, summary)
}
