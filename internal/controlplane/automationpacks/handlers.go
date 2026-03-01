package automationpacks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Handler serves automation pack definition APIs.
type Handler struct {
	store            *Store
	policySimulator  PolicySimulator
	executionRuntime *ExecutionRuntime
	actionRunner     ActionRunner
}

// HandlerOption configures automation-pack handlers.
type HandlerOption func(*Handler)

// WithPolicySimulator injects policy what-if simulation for dry-run planning.
func WithPolicySimulator(simulator PolicySimulator) HandlerOption {
	return func(h *Handler) {
		if simulator != nil {
			h.policySimulator = simulator
		}
	}
}

// WithActionRunner injects execution backing for automation-pack step actions.
func WithActionRunner(runner ActionRunner) HandlerOption {
	return func(h *Handler) {
		if runner != nil {
			h.actionRunner = runner
		}
	}
}

// WithExecutionRuntime overrides the default execution runtime.
func WithExecutionRuntime(runtime *ExecutionRuntime) HandlerOption {
	return func(h *Handler) {
		if runtime != nil {
			h.executionRuntime = runtime
		}
	}
}

func NewHandler(store *Store, opts ...HandlerOption) *Handler {
	h := &Handler{
		store:           store,
		policySimulator: noopPolicySimulator{},
		actionRunner:    noopActionRunner{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	if h.executionRuntime == nil {
		h.executionRuntime = NewExecutionRuntime(store, h.policySimulator, h.actionRunner)
	}
	return h
}

func (h *Handler) HandleCreateDefinition(w http.ResponseWriter, r *http.Request) {
	var def Definition
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&def); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	created, err := h.store.CreateDefinition(def)
	if err != nil {
		switch {
		case errorsAsValidation(err):
			writeError(w, http.StatusBadRequest, "invalid_schema", err.Error())
		case err == ErrAlreadyExists:
			writeError(w, http.StatusConflict, "conflict", "automation pack definition already exists for id/version")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create automation pack definition")
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"automation_pack": created})
}

func (h *Handler) HandleListDefinitions(w http.ResponseWriter, r *http.Request) {
	definitions, err := h.store.ListDefinitions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list automation pack definitions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"automation_packs": definitions})
}

func (h *Handler) HandleGetDefinition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "automation pack id is required")
		return
	}

	version := strings.TrimSpace(r.URL.Query().Get("version"))
	definition, err := h.store.GetDefinition(id, version)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "automation pack definition not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load automation pack definition")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"automation_pack": definition})
}

func (h *Handler) HandleDryRunDefinition(w http.ResponseWriter, r *http.Request) {
	var req DryRunRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	result, err := runDryRun(r.Context(), req, h.policySimulator)
	if err != nil {
		switch {
		case errorsAsValidation(err):
			writeError(w, http.StatusBadRequest, "invalid_schema", err.Error())
		case errorsAsInputValidation(err):
			writeError(w, http.StatusBadRequest, "invalid_inputs", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to dry-run automation pack definition")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"dry_run": result})
}

func (h *Handler) HandleStartExecution(w http.ResponseWriter, r *http.Request) {
	if h.executionRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "automation pack execution unavailable")
		return
	}

	definitionID := strings.TrimSpace(r.PathValue("id"))
	if definitionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "automation pack id is required")
		return
	}

	var req StartExecutionRequest
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
	}
	req.DefinitionID = definitionID

	execution, err := h.executionRuntime.Start(r.Context(), req)
	if err != nil {
		switch {
		case IsNotFound(err):
			writeError(w, http.StatusNotFound, "not_found", "automation pack definition not found")
		case errorsAsValidation(err):
			writeError(w, http.StatusBadRequest, "invalid_schema", err.Error())
		case errorsAsInputValidation(err):
			writeError(w, http.StatusBadRequest, "invalid_inputs", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to execute automation pack")
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"execution": execution})
}

func (h *Handler) HandleGetExecution(w http.ResponseWriter, r *http.Request) {
	if h.executionRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "automation pack execution unavailable")
		return
	}

	executionID := strings.TrimSpace(r.PathValue("executionID"))
	if executionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "execution id is required")
		return
	}

	execution, err := h.executionRuntime.Get(executionID)
	if err != nil {
		if errors.Is(err, ErrExecutionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "automation pack execution not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load automation pack execution")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"execution": execution})
}

func (h *Handler) HandleGetExecutionTimeline(w http.ResponseWriter, r *http.Request) {
	if h.executionRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "automation pack execution unavailable")
		return
	}

	executionID := strings.TrimSpace(r.PathValue("executionID"))
	if executionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "execution id is required")
		return
	}

	timeline, err := h.executionRuntime.GetTimeline(executionID)
	if err != nil {
		if errors.Is(err, ErrExecutionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "automation pack execution not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load automation pack timeline")
		return
	}
	replay, err := h.executionRuntime.GetReplay(executionID)
	if err != nil {
		if errors.Is(err, ErrExecutionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "automation pack execution not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load automation pack replay payload")
		return
	}

	if stepID := strings.TrimSpace(r.URL.Query().Get("step_id")); stepID != "" {
		timeline = filterTimelineByStep(timeline, stepID)
	}
	if eventType := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("type"))); eventType != "" {
		timeline = filterTimelineByType(timeline, eventType)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"execution_id": executionID,
		"timeline":     timeline,
		"replay":       replay,
	})
}

func (h *Handler) HandleGetExecutionArtifacts(w http.ResponseWriter, r *http.Request) {
	if h.executionRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "automation pack execution unavailable")
		return
	}

	executionID := strings.TrimSpace(r.PathValue("executionID"))
	if executionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "execution id is required")
		return
	}

	artifacts, err := h.executionRuntime.GetArtifacts(executionID)
	if err != nil {
		if errors.Is(err, ErrExecutionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "automation pack execution not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load automation pack artifacts")
		return
	}

	if stepID := strings.TrimSpace(r.URL.Query().Get("step_id")); stepID != "" {
		artifacts = filterArtifactsByStep(artifacts, stepID)
	}
	if artifactType := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("type"))); artifactType != "" {
		artifacts = filterArtifactsByType(artifacts, artifactType)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"execution_id": executionID,
		"artifacts":    artifacts,
	})
}

func filterTimelineByStep(timeline []ExecutionTimelineEvent, stepID string) []ExecutionTimelineEvent {
	if stepID == "" || len(timeline) == 0 {
		return timeline
	}
	filtered := make([]ExecutionTimelineEvent, 0, len(timeline))
	for _, item := range timeline {
		if strings.EqualFold(strings.TrimSpace(item.StepID), stepID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterTimelineByType(timeline []ExecutionTimelineEvent, eventType string) []ExecutionTimelineEvent {
	if eventType == "" || len(timeline) == 0 {
		return timeline
	}
	filtered := make([]ExecutionTimelineEvent, 0, len(timeline))
	for _, item := range timeline {
		if strings.EqualFold(strings.TrimSpace(item.Type), eventType) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterArtifactsByStep(artifacts []ExecutionArtifact, stepID string) []ExecutionArtifact {
	if stepID == "" || len(artifacts) == 0 {
		return artifacts
	}
	filtered := make([]ExecutionArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if strings.EqualFold(strings.TrimSpace(artifact.StepID), stepID) {
			filtered = append(filtered, artifact)
		}
	}
	return filtered
}

func filterArtifactsByType(artifacts []ExecutionArtifact, artifactType string) []ExecutionArtifact {
	if artifactType == "" || len(artifacts) == 0 {
		return artifacts
	}
	filtered := make([]ExecutionArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if strings.EqualFold(strings.TrimSpace(artifact.Type), artifactType) {
			filtered = append(filtered, artifact)
		}
	}
	return filtered
}

func errorsAsValidation(err error) bool {
	var validationErr *ValidationError
	return errors.As(err, &validationErr)
}

func errorsAsInputValidation(err error) bool {
	var validationErr *InputValidationError
	return errors.As(err, &validationErr)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"code":  code,
		"error": message,
	})
}
