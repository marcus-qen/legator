package modeldock

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type envProfileResolver func() *Profile

// Handler serves model dock API endpoints.
type Handler struct {
	store      *Store
	providers  *ProviderManager
	envProfile envProfileResolver
}

func NewHandler(store *Store, providers *ProviderManager, envProfile envProfileResolver) *Handler {
	return &Handler{
		store:      store,
		providers:  providers,
		envProfile: envProfile,
	}
}

type profileWriteRequest struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	IsActive bool   `json:"is_active"`
}

func (h *Handler) HandleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.store.ListProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list profiles")
		return
	}

	resp := make([]ProfileResponse, 0, len(profiles))
	for _, profile := range profiles {
		profile.Source = SourceDB
		resp = append(resp, profile.ToResponse())
	}

	writeJSON(w, http.StatusOK, map[string]any{"profiles": resp})
}

func (h *Handler) HandleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var req profileWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if msg := validateProfileRequest(req, true); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}

	created, err := h.store.CreateProfile(Profile{
		Name:     strings.TrimSpace(req.Name),
		Provider: strings.TrimSpace(req.Provider),
		BaseURL:  strings.TrimSpace(req.BaseURL),
		Model:    strings.TrimSpace(req.Model),
		APIKey:   strings.TrimSpace(req.APIKey),
		IsActive: req.IsActive,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if created.IsActive && h.providers != nil {
		_ = h.providers.ActivateProfile(created)
	}

	writeJSON(w, http.StatusCreated, map[string]any{"profile": created.ToResponse()})
}

func (h *Handler) HandleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "profile id required")
		return
	}

	var req profileWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if msg := validateProfileRequest(req, false); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}

	updated, err := h.store.UpdateProfile(id, Profile{
		Name:     strings.TrimSpace(req.Name),
		Provider: strings.TrimSpace(req.Provider),
		BaseURL:  strings.TrimSpace(req.BaseURL),
		Model:    strings.TrimSpace(req.Model),
		APIKey:   strings.TrimSpace(req.APIKey),
		IsActive: req.IsActive,
	})
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "profile not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if updated.IsActive && h.providers != nil {
		_ = h.providers.ActivateProfile(updated)
	}

	writeJSON(w, http.StatusOK, map[string]any{"profile": updated.ToResponse()})
}

func (h *Handler) HandleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "profile id required")
		return
	}

	target, err := h.store.GetProfile(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "profile not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load profile")
		return
	}

	envFallback := h.providers != nil && h.providers.HasEnvFallback()
	if target.IsActive {
		hasOtherActive, err := h.store.HasActiveExcluding(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to validate active profile")
			return
		}
		if !hasOtherActive && !envFallback {
			writeError(w, http.StatusConflict, "conflict", "cannot delete active profile without another active profile or env fallback")
			return
		}
	}

	if err := h.store.DeleteProfile(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "profile not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete profile")
		return
	}

	if target.IsActive && h.providers != nil {
		if active, err := h.store.GetActiveProfile(); err == nil {
			_ = h.providers.ActivateProfile(active)
		} else {
			_ = h.providers.UseEnvFallback()
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

func (h *Handler) HandleActivateProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "profile id required")
		return
	}

	profile, err := h.store.ActivateProfile(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "profile not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if h.providers != nil {
		if err := h.providers.ActivateProfile(profile); err != nil {
			writeError(w, http.StatusBadGateway, "llm_unavailable", err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "activated",
		"profile": profile.ToResponse(),
	})
}

func (h *Handler) HandleGetActiveProfile(w http.ResponseWriter, r *http.Request) {
	hasProfiles, err := h.store.HasProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read profile state")
		return
	}

	if !hasProfiles {
		env := h.resolveEnvProfile()
		if env == nil {
			writeError(w, http.StatusNotFound, "not_found", "no active model profile")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"profile": env.ToResponse()})
		return
	}

	active, err := h.store.GetActiveProfile()
	if err == nil {
		active.Source = SourceDB
		writeJSON(w, http.StatusOK, map[string]any{"profile": active.ToResponse()})
		return
	}

	snapshot := ProviderSnapshot{}
	if h.providers != nil {
		snapshot = h.providers.Snapshot()
	}
	if snapshot.Source == SourceEnv {
		env := h.resolveEnvProfile()
		if env != nil {
			writeJSON(w, http.StatusOK, map[string]any{"profile": env.ToResponse()})
			return
		}
	}

	writeError(w, http.StatusNotFound, "not_found", "no active model profile")
}

func (h *Handler) HandleGetUsage(w http.ResponseWriter, r *http.Request) {
	window, err := parseWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	items, totals, since, err := h.store.AggregateUsage(window)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to aggregate usage")
		return
	}

	env := h.resolveEnvProfile()
	for idx := range items {
		if items[idx].ProfileID == EnvProfileID && items[idx].ProfileName == "" && env != nil {
			items[idx].ProfileName = env.Name
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"window": window.String(),
		"since":  since.Format(time.RFC3339),
		"totals": totals,
		"usage":  items,
	})
}

func (h *Handler) resolveEnvProfile() *Profile {
	if h.envProfile == nil {
		return nil
	}
	profile := h.envProfile()
	if profile == nil {
		return nil
	}
	profile.Source = SourceEnv
	profile.IsActive = true
	if profile.ID == "" {
		profile.ID = EnvProfileID
	}
	return profile
}

func parseWindow(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 24 * time.Hour, nil
	}
	window, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if window <= 0 {
		return 0, errInvalidWindow
	}
	if window > 90*24*time.Hour {
		return 0, errInvalidWindow
	}
	return window, nil
}

var errInvalidWindow = &requestError{message: "window must be a positive duration <= 2160h"}

type requestError struct {
	message string
}

func (e *requestError) Error() string { return e.message }

func validateProfileRequest(req profileWriteRequest, requireAPIKey bool) string {
	if strings.TrimSpace(req.Name) == "" {
		return "name is required"
	}
	if strings.TrimSpace(req.Provider) == "" {
		return "provider is required"
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		return "base_url is required"
	}
	if strings.TrimSpace(req.Model) == "" {
		return "model is required"
	}
	if requireAPIKey && strings.TrimSpace(req.APIKey) == "" {
		return "api_key is required"
	}
	return ""
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
