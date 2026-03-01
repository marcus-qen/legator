package automationpacks

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Handler serves automation pack definition APIs.
type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
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

func errorsAsValidation(err error) bool {
	var validationErr *ValidationError
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
