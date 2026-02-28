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
	store     *Store
	scheduler *Scheduler
}

// NewHandler creates a jobs API handler.
func NewHandler(store *Store, scheduler *Scheduler) *Handler {
	return &Handler{store: store, scheduler: scheduler}
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
		Name     string `json:"name"`
		Command  string `json:"command"`
		Schedule string `json:"schedule"`
		Target   Target `json:"target"`
		Enabled  *bool  `json:"enabled"`
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
		Name:       strings.TrimSpace(req.Name),
		Command:    strings.TrimSpace(req.Command),
		Schedule:   strings.TrimSpace(req.Schedule),
		Target:     req.Target,
		Enabled:    enabled,
		LastStatus: "",
	}
	created, err := h.store.CreateJob(job)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_job", err.Error())
		return
	}

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
		Name     string `json:"name"`
		Command  string `json:"command"`
		Schedule string `json:"schedule"`
		Target   Target `json:"target"`
		Enabled  *bool  `json:"enabled"`
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

	updated, err := h.store.UpdateJob(Job{
		ID:         id,
		Name:       strings.TrimSpace(req.Name),
		Command:    strings.TrimSpace(req.Command),
		Schedule:   strings.TrimSpace(req.Schedule),
		Target:     req.Target,
		Enabled:    enabled,
		CreatedAt:  existing.CreatedAt,
		LastRunAt:  existing.LastRunAt,
		LastStatus: existing.LastStatus,
	})
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "job not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_job", err.Error())
		return
	}

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

	if err := h.scheduler.TriggerNow(id); err != nil {
		writeError(w, http.StatusBadGateway, "dispatch_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "dispatched", "job_id": id})
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
		"job_id":        id,
		"runs":          runs,
		"count":         len(runs),
		"failed_count":  summary.Failed,
		"success_count": summary.Success,
		"running_count": summary.Running,
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
		"runs":          runs,
		"count":         len(runs),
		"failed_count":  summary.Failed,
		"success_count": summary.Success,
		"running_count": summary.Running,
	})
}

// HandleEnableJob serves POST /api/v1/jobs/{id}/enable.
func (h *Handler) HandleEnableJob(w http.ResponseWriter, r *http.Request) {
	handleToggleJob(w, r, h.store, true)
}

// HandleDisableJob serves POST /api/v1/jobs/{id}/disable.
func (h *Handler) HandleDisableJob(w http.ResponseWriter, r *http.Request) {
	handleToggleJob(w, r, h.store, false)
}

func handleToggleJob(w http.ResponseWriter, r *http.Request, store *Store, enabled bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing job id")
		return
	}
	job, err := store.SetEnabled(id, enabled)
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

type runSummary struct {
	Running int
	Success int
	Failed  int
}

func summarizeRuns(runs []JobRun) runSummary {
	summary := runSummary{}
	for _, run := range runs {
		switch run.Status {
		case RunStatusRunning:
			summary.Running++
		case RunStatusSuccess:
			summary.Success++
		case RunStatusFailed:
			summary.Failed++
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
		case RunStatusRunning, RunStatusSuccess, RunStatusFailed:
			query.Status = status
		default:
			return RunQuery{}, fmt.Errorf("status must be one of: running, success, failed")
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
