package jobs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler exposes HTTP endpoints for scheduled jobs.
type Handler struct {
	store             *Store
	scheduler         *Scheduler
	lifecycleObserver LifecycleObserver
}

type HandlerOption func(*Handler)

// WithHandlerLifecycleObserver wires lifecycle event notifications for job mutation APIs.
func WithHandlerLifecycleObserver(observer LifecycleObserver) HandlerOption {
	return func(h *Handler) {
		if observer == nil {
			h.lifecycleObserver = noopLifecycleObserver{}
			return
		}
		h.lifecycleObserver = observer
	}
}

// NewHandler creates a jobs API handler.
func NewHandler(store *Store, scheduler *Scheduler, opts ...HandlerOption) *Handler {
	h := &Handler{store: store, scheduler: scheduler, lifecycleObserver: noopLifecycleObserver{}}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return h
}

// HandleListJobs serves GET /api/v1/jobs.
func (h *Handler) HandleListJobs(w http.ResponseWriter, _ *http.Request) {
	jobs, err := h.store.ListJobs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

// HandleCreateJob serves POST /api/v1/jobs.
func (h *Handler) HandleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string       `json:"name"`
		Command     string       `json:"command"`
		Schedule    string       `json:"schedule"`
		Target      Target       `json:"target"`
		RetryPolicy *RetryPolicy `json:"retry_policy"`
		Enabled     *bool        `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if err := validateSchedule(req.Schedule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_schedule", err.Error())
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	job := Job{
		Name:        strings.TrimSpace(req.Name),
		Command:     strings.TrimSpace(req.Command),
		Schedule:    strings.TrimSpace(req.Schedule),
		Target:      req.Target,
		RetryPolicy: req.RetryPolicy,
		Enabled:     enabled,
		LastStatus:  "",
	}
	created, err := h.store.CreateJob(job)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_job", err.Error())
		return
	}

	h.emitLifecycleEvent(LifecycleEvent{Type: EventJobCreated, Actor: "api", JobID: created.ID})
	writeJSON(w, http.StatusCreated, created)
}

// HandleGetJob serves GET /api/v1/jobs/{id}.
func (h *Handler) HandleGetJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}

	job, err := h.store.GetJob(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// HandleUpdateJob serves PUT /api/v1/jobs/{id}.
func (h *Handler) HandleUpdateJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}

	existing, err := h.store.GetJob(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	var req struct {
		Name        string       `json:"name"`
		Command     string       `json:"command"`
		Schedule    string       `json:"schedule"`
		Target      Target       `json:"target"`
		RetryPolicy *RetryPolicy `json:"retry_policy"`
		Enabled     *bool        `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if err := validateSchedule(req.Schedule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_schedule", err.Error())
		return
	}

	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	retryPolicy := existing.RetryPolicy
	if req.RetryPolicy != nil {
		retryPolicy = req.RetryPolicy
	}

	updated, err := h.store.UpdateJob(Job{
		ID:          id,
		Name:        strings.TrimSpace(req.Name),
		Command:     strings.TrimSpace(req.Command),
		Schedule:    strings.TrimSpace(req.Schedule),
		Target:      req.Target,
		RetryPolicy: retryPolicy,
		Enabled:     enabled,
		CreatedAt:   existing.CreatedAt,
		LastRunAt:   existing.LastRunAt,
		LastStatus:  existing.LastStatus,
	})
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_job", err.Error())
		return
	}

	h.emitLifecycleEvent(LifecycleEvent{Type: EventJobUpdated, Actor: "api", JobID: updated.ID})
	writeJSON(w, http.StatusOK, updated)
}

// HandleDeleteJob serves DELETE /api/v1/jobs/{id}.
func (h *Handler) HandleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}
	if err := h.store.DeleteJob(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.emitLifecycleEvent(LifecycleEvent{Type: EventJobDeleted, Actor: "api", JobID: id})
	w.WriteHeader(http.StatusNoContent)
}

// HandleRunJob serves POST /api/v1/jobs/{id}/run.
func (h *Handler) HandleRunJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}
	if _, err := h.store.GetJob(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if h.scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "jobs scheduler unavailable")
		return
	}

	if err := h.scheduler.TriggerNow(id); err != nil {
		writeError(w, http.StatusBadGateway, "dispatch_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "dispatched", "job_id": id})
}

// HandleCancelJob serves POST /api/v1/jobs/{id}/cancel.
func (h *Handler) HandleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}
	if _, err := h.store.GetJob(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if h.scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "jobs scheduler unavailable")
		return
	}

	summary, err := h.scheduler.CancelJob(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":                id,
		"status":                "cancel_requested",
		"canceled_runs":         summary.CanceledRuns,
		"already_terminal_runs": summary.AlreadyTerminalRuns,
		"canceled_retries":      summary.CanceledRetries,
	})
}

// HandleCancelRun serves POST /api/v1/jobs/{id}/runs/{runId}/cancel.
func (h *Handler) HandleCancelRun(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("id"))
	runID := strings.TrimSpace(r.PathValue("runId"))
	if jobID == "" || runID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id or run id")
		return
	}
	if h.scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "jobs scheduler unavailable")
		return
	}

	run, err := h.scheduler.CancelRun(jobID, runID)
	if err != nil {
		switch {
		case IsNotFound(err):
			writeError(w, http.StatusNotFound, "not_found", "run not found")
		case IsInvalidRunTransition(err):
			writeError(w, http.StatusConflict, "invalid_transition", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"job_id": jobID,
		"run":    run,
	})
}

// HandleRetryRun serves POST /api/v1/jobs/{id}/runs/{runId}/retry.
// Retry is intentionally minimal: it validates the referenced run and
// dispatches a new immediate job execution using the existing TriggerNow flow.
func (h *Handler) HandleRetryRun(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("id"))
	runID := strings.TrimSpace(r.PathValue("runId"))
	if jobID == "" || runID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id or run id")
		return
	}
	if h.scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "jobs scheduler unavailable")
		return
	}

	run, err := h.store.GetRun(runID)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	if strings.TrimSpace(run.JobID) != jobID {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}

	if run.Status != RunStatusFailed && run.Status != RunStatusCanceled && run.Status != RunStatusDenied {
		writeError(w, http.StatusConflict, "invalid_transition", "only failed, canceled, or denied runs can be retried")
		return
	}

	if err := h.scheduler.TriggerNow(jobID); err != nil {
		writeError(w, http.StatusBadGateway, "dispatch_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":        jobID,
		"source_run_id": runID,
		"status":        "retry_dispatched",
	})
}

// HandleListRuns serves GET /api/v1/jobs/{id}/runs.
func (h *Handler) HandleListRuns(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}
	if _, err := h.store.GetJob(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	query, err := parseRunQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	query.JobID = id

	runs, err := h.store.ListRuns(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	summary := summarizeRuns(runs)
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":         id,
		"runs":           runs,
		"count":          len(runs),
		"failed_count":   summary.Failed,
		"success_count":  summary.Success,
		"running_count":  summary.Running,
		"pending_count":  summary.Pending,
		"queued_count":   summary.Queued,
		"canceled_count": summary.Canceled,
		"denied_count":   summary.Denied,
	})
}

// HandleListAllRuns serves GET /api/v1/jobs/runs.
func (h *Handler) HandleListAllRuns(w http.ResponseWriter, r *http.Request) {
	query, err := parseRunQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if jobID := strings.TrimSpace(r.URL.Query().Get("job_id")); jobID != "" {
		if _, err := h.store.GetJob(jobID); err != nil {
			if IsNotFound(err) {
				writeError(w, http.StatusNotFound, "not_found", "job not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		query.JobID = jobID
	}

	runs, err := h.store.ListRuns(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	summary := summarizeRuns(runs)
	writeJSON(w, http.StatusOK, map[string]any{
		"runs":           runs,
		"count":          len(runs),
		"failed_count":   summary.Failed,
		"success_count":  summary.Success,
		"running_count":  summary.Running,
		"pending_count":  summary.Pending,
		"queued_count":   summary.Queued,
		"canceled_count": summary.Canceled,
		"denied_count":   summary.Denied,
	})
}

// HandleEnableJob serves POST /api/v1/jobs/{id}/enable.
func (h *Handler) HandleEnableJob(w http.ResponseWriter, r *http.Request) {
	handleToggleJob(w, r, h, true)
}

// HandleDisableJob serves POST /api/v1/jobs/{id}/disable.
func (h *Handler) HandleDisableJob(w http.ResponseWriter, r *http.Request) {
	handleToggleJob(w, r, h, false)
}

func handleToggleJob(w http.ResponseWriter, r *http.Request, handler *Handler, enabled bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}
	job, err := handler.store.SetEnabled(id, enabled)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	handler.emitLifecycleEvent(LifecycleEvent{Type: EventJobUpdated, Actor: "api", JobID: job.ID})
	writeJSON(w, http.StatusOK, job)
}

type runSummary struct {
	Queued   int
	Pending  int
	Running  int
	Success  int
	Failed   int
	Canceled int
	Denied   int
}

func summarizeRuns(runs []JobRun) runSummary {
	summary := runSummary{}
	for _, run := range runs {
		switch run.Status {
		case RunStatusQueued:
			summary.Queued++
		case RunStatusPending:
			summary.Pending++
		case RunStatusRunning:
			summary.Running++
		case RunStatusSuccess:
			summary.Success++
		case RunStatusFailed:
			summary.Failed++
		case RunStatusCanceled:
			summary.Canceled++
		case RunStatusDenied:
			summary.Denied++
		}
	}
	return summary
}

func parseRunQuery(r *http.Request) (RunQuery, error) {
	query := RunQuery{}

	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return RunQuery{}, fmt.Errorf("limit must be a positive integer")
		}
		query.Limit = parsed
	}

	query.ProbeID = strings.TrimSpace(r.URL.Query().Get("probe_id"))

	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		switch status {
		case RunStatusQueued, RunStatusPending, RunStatusRunning, RunStatusSuccess, RunStatusFailed, RunStatusCanceled, RunStatusDenied:
			query.Status = status
		default:
			return RunQuery{}, fmt.Errorf("status must be one of: queued, pending, running, success, failed, canceled, denied")
		}
	}

	if raw := strings.TrimSpace(r.URL.Query().Get("started_after")); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return RunQuery{}, fmt.Errorf("started_after must be RFC3339")
		}
		query.StartedAfter = &ts
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("started_before")); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return RunQuery{}, fmt.Errorf("started_before must be RFC3339")
		}
		query.StartedBefore = &ts
	}
	if query.StartedAfter != nil && query.StartedBefore != nil && query.StartedAfter.After(*query.StartedBefore) {
		return RunQuery{}, fmt.Errorf("started_after must be <= started_before")
	}

	return query, nil
}

func validateSchedule(schedule string) error {
	_, err := isScheduleDue(schedule, nil, time.Now().UTC(), time.Now().UTC())
	return err
}

func (h *Handler) emitLifecycleEvent(evt LifecycleEvent) {
	if h == nil || h.lifecycleObserver == nil {
		return
	}
	h.lifecycleObserver.ObserveJobLifecycleEvent(evt.normalize())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
