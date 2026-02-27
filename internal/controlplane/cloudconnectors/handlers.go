package cloudconnectors

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler serves cloud connector APIs.
type Handler struct {
	store   *Store
	scanner Scanner
}

func NewHandler(store *Store, scanner Scanner) *Handler {
	if scanner == nil {
		scanner = NewCLIAdapter()
	}
	return &Handler{store: store, scanner: scanner}
}

type connectorWriteRequest struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	AuthMode  string `json:"auth_mode"`
	IsEnabled *bool  `json:"is_enabled"`
}

func (h *Handler) HandleListConnectors(w http.ResponseWriter, r *http.Request) {
	connectors, err := h.store.ListConnectors()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list connectors")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": connectors})
}

func (h *Handler) HandleCreateConnector(w http.ResponseWriter, r *http.Request) {
	var req connectorWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if msg := validateConnectorRequest(req, true); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}

	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}

	connector, err := h.store.CreateConnector(Connector{
		Name:      strings.TrimSpace(req.Name),
		Provider:  normalizeProvider(req.Provider),
		AuthMode:  normalizeAuthMode(req.AuthMode),
		IsEnabled: isEnabled,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"connector": connector})
}

func (h *Handler) HandleUpdateConnector(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "connector id required")
		return
	}

	existing, err := h.store.GetConnector(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "connector not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load connector")
		return
	}

	var req connectorWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if msg := validateConnectorRequest(req, false); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}

	isEnabled := existing.IsEnabled
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = existing.Name
	}
	provider := normalizeProvider(req.Provider)
	if provider == "" {
		provider = existing.Provider
	}
	authMode := normalizeAuthMode(req.AuthMode)
	if authMode == "" {
		authMode = existing.AuthMode
	}

	updated, err := h.store.UpdateConnector(id, Connector{
		Name:      name,
		Provider:  provider,
		AuthMode:  authMode,
		IsEnabled: isEnabled,
	})
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "connector not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"connector": updated})
}

func (h *Handler) HandleDeleteConnector(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "connector id required")
		return
	}

	if err := h.store.DeleteConnector(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "connector not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete connector")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

func (h *Handler) HandleScanConnector(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "connector id required")
		return
	}

	connector, err := h.store.GetConnector(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "connector not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load connector")
		return
	}

	scannedAt := time.Now().UTC()
	assets, err := h.scanner.Scan(r.Context(), *connector)
	if err != nil {
		_ = h.store.SetConnectorScanResult(connector.ID, ScanStatusError, err.Error(), scannedAt)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code":      "scan_failed",
			"error":     err.Error(),
			"connector": connector,
		})
		return
	}

	if err := h.store.ReplaceAssetsForConnector(*connector, assets); err != nil {
		_ = h.store.SetConnectorScanResult(connector.ID, ScanStatusError, err.Error(), scannedAt)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to persist assets")
		return
	}

	if err := h.store.SetConnectorScanResult(connector.ID, ScanStatusSuccess, "", scannedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to persist scan status")
		return
	}

	updated, err := h.store.GetConnector(connector.ID)
	if err != nil {
		updated = connector
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"connector":         updated,
		"assets_discovered": len(assets),
	})
}

func (h *Handler) HandleListAssets(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = value
	}

	assets, err := h.store.ListAssets(AssetFilter{
		Provider:    normalizeProvider(r.URL.Query().Get("provider")),
		ConnectorID: strings.TrimSpace(r.URL.Query().Get("connector_id")),
		Limit:       limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list assets")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
}

func validateConnectorRequest(req connectorWriteRequest, requireFields bool) string {
	name := strings.TrimSpace(req.Name)
	provider := normalizeProvider(req.Provider)
	authMode := normalizeAuthMode(req.AuthMode)

	if requireFields && name == "" {
		return "name is required"
	}
	if requireFields && provider == "" {
		return "provider is required"
	}

	if provider != "" && !isSupportedProvider(provider) {
		return "provider must be one of: aws, gcp, azure"
	}
	if authMode != "" && authMode != AuthModeCLI {
		return "auth_mode must be cli"
	}
	return ""
}

func isSupportedProvider(provider string) bool {
	switch normalizeProvider(provider) {
	case ProviderAWS, ProviderGCP, ProviderAzure:
		return true
	default:
		return false
	}
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
