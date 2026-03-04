package networkdevices

// HTTP handlers for the enrichment endpoints:
//   POST /api/v1/network-devices/{id}/enrich      — trigger NETCONF+SNMP enrichment
//   GET  /api/v1/network-devices/{id}/interfaces  — return stored interface detail

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HandleEnrichDevice handles POST /api/v1/network-devices/{id}/enrich.
// Body: EnrichRequest JSON (netconf and/or snmp config).
func (h *Handler) HandleEnrichDevice(w http.ResponseWriter, r *http.Request) {
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

	var req EnrichRequest
	if r.Body != nil {
		defer r.Body.Close()
		if decErr := json.NewDecoder(r.Body).Decode(&req); decErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
	}

	if req.Netconf == nil && req.SNMP == nil {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"at least one of 'netconf' or 'snmp' must be configured in request body")
		return
	}

	enricher := NewEnricher(h.store, EnricherOptions{})
	result, err := enricher.Enrich(r.Context(), *device, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "enrichment_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"inventory": result})
}

// HandleGetInterfaces handles GET /api/v1/network-devices/{id}/interfaces.
func (h *Handler) HandleGetInterfaces(w http.ResponseWriter, r *http.Request) {
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

	ifaces, err := h.store.GetInterfaceDetails(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found",
				"no enriched inventory available — run /enrich first")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load interface details")
		return
	}

	if ifaces == nil {
		ifaces = []InterfaceDetail{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": ifaces, "device_id": id})
}
