package networkdevices

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HandleCommandDevice handles POST /api/v1/network/devices/{id}/command.
func (h *Handler) HandleCommandDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "device id required")
		return
	}

	device, err := h.store.GetDevice(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "network device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load network device")
		return
	}

	var req CommandRequest
	if decErr := json.NewDecoder(r.Body).Decode(&req); decErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "command is required")
		return
	}

	creds := CredentialInput{
		Password:   strings.TrimSpace(req.Password),
		PrivateKey: strings.TrimSpace(req.PrivateKey),
	}

	executor := NewSSHExecutor(h.store)
	result, err := executor.Execute(r.Context(), *device, creds, req.Command)
	if err != nil {
		writeError(w, http.StatusBadGateway, "execution_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

// HandleScanDevice handles POST /api/v1/network/devices/{id}/scan.
func (h *Handler) HandleScanDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "device id required")
		return
	}

	device, err := h.store.GetDevice(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "network device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load network device")
		return
	}

	type scanRequest struct {
		Password       string `json:"password,omitempty"`
		PrivateKey     string `json:"private_key,omitempty"`
		IncludeRouting bool   `json:"include_routing,omitempty"`
	}
	var req scanRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	creds := CredentialInput{
		Password:   strings.TrimSpace(req.Password),
		PrivateKey: strings.TrimSpace(req.PrivateKey),
	}
	cfg := ScanConfig{IncludeRouting: req.IncludeRouting}

	executor := NewSSHExecutor(h.store)
	scanner := NewScanner(executor, h.store)

	result, err := scanner.Scan(r.Context(), *device, creds, cfg)
	if err != nil {
		writeError(w, http.StatusBadGateway, "scan_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"inventory": result})
}

// HandleGetInventory handles GET /api/v1/network/devices/{id}/inventory.
func (h *Handler) HandleGetInventory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "device id required")
		return
	}

	if _, err := h.store.GetDevice(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "network device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load network device")
		return
	}

	result, err := h.store.GetLatestInventory(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "no inventory available — run a scan first")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load inventory")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"inventory": result})
}
