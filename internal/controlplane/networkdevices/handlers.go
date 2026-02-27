package networkdevices

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Handler serves network device CRUD + probe endpoints.
type Handler struct {
	store  *Store
	prober Prober
}

func NewHandler(store *Store, prober Prober) *Handler {
	if prober == nil {
		prober = NewSSHProber()
	}
	return &Handler{store: store, prober: prober}
}

type deviceWriteRequest struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Vendor   string   `json:"vendor"`
	Username string   `json:"username"`
	AuthMode string   `json:"auth_mode"`
	Tags     []string `json:"tags"`
}

func (h *Handler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := h.store.ListDevices()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list network devices")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (h *Handler) HandleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if msg := validateWriteRequest(req, true); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}

	device, err := h.store.CreateDevice(Device{
		Name:     strings.TrimSpace(req.Name),
		Host:     strings.TrimSpace(req.Host),
		Port:     normalizePort(req.Port),
		Vendor:   normalizeVendor(req.Vendor),
		Username: strings.TrimSpace(req.Username),
		AuthMode: normalizeAuthMode(req.AuthMode),
		Tags:     normalizeTags(req.Tags),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"device": device})
}

func (h *Handler) HandleGetDevice(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, http.StatusOK, map[string]any{"device": device})
}

func (h *Handler) HandleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "device id required")
		return
	}

	var req deviceWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if msg := validateWriteRequest(req, false); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}

	device, err := h.store.UpdateDevice(id, Device{
		Name:     strings.TrimSpace(req.Name),
		Host:     strings.TrimSpace(req.Host),
		Port:     req.Port,
		Vendor:   strings.TrimSpace(req.Vendor),
		Username: strings.TrimSpace(req.Username),
		AuthMode: strings.TrimSpace(req.AuthMode),
		Tags:     req.Tags,
	})
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "network device not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"device": device})
}

func (h *Handler) HandleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "device id required")
		return
	}
	if err := h.store.DeleteDevice(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "network device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete network device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

func (h *Handler) HandleTestDevice(w http.ResponseWriter, r *http.Request) {
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

	creds, err := decodeCredentialInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	result, err := h.prober.Test(r.Context(), *device, creds)
	if err != nil {
		writeError(w, http.StatusBadGateway, "probe_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (h *Handler) HandleInventoryDevice(w http.ResponseWriter, r *http.Request) {
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

	creds, err := decodeCredentialInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	result, err := h.prober.Inventory(r.Context(), *device, creds)
	if err != nil {
		writeError(w, http.StatusBadGateway, "inventory_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"inventory": result})
}

func decodeCredentialInput(r *http.Request) (CredentialInput, error) {
	if r.Body == nil {
		return CredentialInput{}, nil
	}
	defer r.Body.Close()

	var creds CredentialInput
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&creds); err != nil {
		if errors.Is(err, io.EOF) {
			return CredentialInput{}, nil
		}
		return CredentialInput{}, err
	}
	creds.Password = strings.TrimSpace(creds.Password)
	creds.PrivateKey = strings.TrimSpace(creds.PrivateKey)
	return creds, nil
}

func validateWriteRequest(req deviceWriteRequest, requireFields bool) string {
	name := strings.TrimSpace(req.Name)
	host := strings.TrimSpace(req.Host)
	vendor := normalizeVendor(req.Vendor)
	username := strings.TrimSpace(req.Username)
	authMode := normalizeAuthMode(req.AuthMode)

	if requireFields && name == "" {
		return "name is required"
	}
	if requireFields && host == "" {
		return "host is required"
	}
	if requireFields && username == "" {
		return "username is required"
	}
	if req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		return "port must be between 1 and 65535"
	}
	if vendor != "" && !supportedVendor(vendor) {
		return "vendor must be one of: cisco, junos, fortinet, generic"
	}
	if authMode != "" && !supportedAuthMode(authMode) {
		return "auth_mode must be one of: password, agent, key"
	}
	return ""
}

func supportedVendor(vendor string) bool {
	switch normalizeVendor(vendor) {
	case VendorCisco, VendorJunos, VendorFortinet, VendorGeneric:
		return true
	default:
		return false
	}
}

func supportedAuthMode(mode string) bool {
	switch normalizeAuthMode(mode) {
	case AuthModePassword, AuthModeAgent, AuthModeKey:
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
