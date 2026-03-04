package modeldock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
)

// HandleCreateTrial creates a new trial definition.
// POST /api/v1/modeldock/trials
func (h *Handler) HandleCreateTrial(w http.ResponseWriter, r *http.Request) {
	if h.trialStore == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "trial store not available")
		return
	}
	var req struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Prompts     []TrialPrompt   `json:"prompts"`
		Models      []TrialModel    `json:"models"`
		Parameters  TrialParameters `json:"parameters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if len(req.Prompts) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one prompt is required")
		return
	}
	if len(req.Models) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one model is required")
		return
	}

	// Ensure all prompts have IDs.
	for i := range req.Prompts {
		if strings.TrimSpace(req.Prompts[i].ID) == "" {
			req.Prompts[i].ID = fmt.Sprintf("p%d", i+1)
		}
	}

	trial, err := h.trialStore.CreateTrial(Trial{
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		Prompts:     req.Prompts,
		Models:      req.Models,
		Parameters:  req.Parameters,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"trial": trial})
}

// HandleListTrials lists all trial definitions.
// GET /api/v1/modeldock/trials
func (h *Handler) HandleListTrials(w http.ResponseWriter, r *http.Request) {
	if h.trialStore == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "trial store not available")
		return
	}
	trials, err := h.trialStore.ListTrials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trials": trials})
}

// HandleRunTrial executes a trial and stores the results.
// POST /api/v1/modeldock/trials/{id}/run
func (h *Handler) HandleRunTrial(w http.ResponseWriter, r *http.Request) {
	if h.trialStore == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "trial store not available")
		return
	}

	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "trial id required")
		return
	}

	trial, err := h.trialStore.GetTrial(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "trial not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Build a provider map from model configurations.
	providerMap, buildErr := h.buildProviderMap(trial.Models)
	if buildErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", buildErr.Error())
		return
	}
	if len(providerMap) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "no configured models found for this trial")
		return
	}

	run, err := h.trialStore.CreateRun(trial.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	_ = h.trialStore.UpdateRunStatus(run.ID, TrialRunRunning, "")

	executor := NewTrialExecutor(providerMap)
	results, execErr := executor.Execute(context.Background(), trial)

	if execErr != nil {
		_ = h.trialStore.UpdateRunStatus(run.ID, TrialRunFailed, execErr.Error())
		writeError(w, http.StatusInternalServerError, "execution_error", execErr.Error())
		return
	}

	// Persist results.
	for i := range results {
		results[i].RunID = run.ID
		if _, saveErr := h.trialStore.SaveResult(results[i]); saveErr != nil {
			_ = saveErr
		}
	}

	_ = h.trialStore.UpdateRunStatus(run.ID, TrialRunCompleted, "")
	run, _ = h.trialStore.GetRun(run.ID)

	writeJSON(w, http.StatusOK, map[string]any{
		"run":     run,
		"results": results,
	})
}

// HandleGetTrialResults returns results for a trial's most recent (or specified) run.
// GET /api/v1/modeldock/trials/{id}/results
func (h *Handler) HandleGetTrialResults(w http.ResponseWriter, r *http.Request) {
	if h.trialStore == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "trial store not available")
		return
	}

	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "trial id required")
		return
	}

	if _, err := h.trialStore.GetTrial(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "trial not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	runs, err := h.trialStore.ListRuns(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" && len(runs) > 0 {
		runID = runs[0].ID
	}

	var results []TrialResult
	if runID != "" {
		results, err = h.trialStore.ListResults(runID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	} else {
		results = []TrialResult{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"trial_id": id,
		"runs":     runs,
		"run_id":   runID,
		"results":  results,
	})
}

// HandleCompareTrialResults returns a side-by-side comparison report for a trial run.
// GET /api/v1/modeldock/trials/{id}/compare
func (h *Handler) HandleCompareTrialResults(w http.ResponseWriter, r *http.Request) {
	if h.trialStore == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "trial store not available")
		return
	}

	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "trial id required")
		return
	}

	trial, err := h.trialStore.GetTrial(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "trial not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	runs, err := h.trialStore.ListRuns(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" && len(runs) > 0 {
		runID = runs[0].ID
	}
	if runID == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"trial_id": id,
			"report":   nil,
			"message":  "no runs found for this trial",
		})
		return
	}

	aggs, err := h.trialStore.AggregateByModel(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	report := BuildCompareReport(runID, id, trial, aggs)
	writeJSON(w, http.StatusOK, map[string]any{
		"trial_id": id,
		"report":   report,
	})
}

// buildProviderMap resolves profile IDs to TrialLLMClient instances.
func (h *Handler) buildProviderMap(models []TrialModel) (map[string]TrialLLMClient, error) {
	out := make(map[string]TrialLLMClient, len(models))
	for _, m := range models {
		if _, exists := out[m.ProfileID]; exists {
			continue
		}
		profile, err := h.store.GetProfile(m.ProfileID)
		if err != nil {
			return nil, fmt.Errorf("profile %q not found: %w", m.ProfileID, err)
		}
		cfg := llm.ProviderConfig{
			Name:    profile.Provider,
			BaseURL: profile.BaseURL,
			APIKey:  profile.APIKey,
			Model:   profile.Model,
		}
		out[m.ProfileID] = llm.NewOpenAIProvider(cfg)
	}
	return out, nil
}
