package discovery

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/marcus-qen/legator/internal/protocol"
)

// CandidateHandler exposes HTTP endpoints for managing deployment candidates
// and processes incoming discovery-report WebSocket messages from probes.
type CandidateHandler struct {
	store *CandidateStore
}

// NewCandidateHandler creates a CandidateHandler backed by the given store.
func NewCandidateHandler(store *CandidateStore) *CandidateHandler {
	return &CandidateHandler{store: store}
}

// HandleListCandidates serves GET /api/v1/discovery/candidates.
// Optional ?status= query param filters by candidate status.
func (h *CandidateHandler) HandleListCandidates(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "candidate store unavailable")
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	candidates, err := h.store.List(status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list candidates")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": candidates,
		"total":      len(candidates),
	})
}

// HandleGetCandidate serves GET /api/v1/discovery/candidates/{id}.
func (h *CandidateHandler) HandleGetCandidate(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "candidate store unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "candidate id required")
		return
	}

	c, err := h.store.Get(id)
	if err != nil {
		if IsCandidateNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "candidate not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load candidate")
		return
	}

	writeJSON(w, http.StatusOK, c)
}

// HandleApproveCandidate serves POST /api/v1/discovery/candidates/{id}/approve.
func (h *CandidateHandler) HandleApproveCandidate(w http.ResponseWriter, r *http.Request) {
	h.handleTransition(w, r, CandidateStatusApproved)
}

// HandleRejectCandidate serves POST /api/v1/discovery/candidates/{id}/reject.
func (h *CandidateHandler) HandleRejectCandidate(w http.ResponseWriter, r *http.Request) {
	h.handleTransition(w, r, CandidateStatusRejected)
}

func (h *CandidateHandler) handleTransition(w http.ResponseWriter, r *http.Request, newStatus string) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "candidate store unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "candidate id required")
		return
	}

	if err := h.store.Transition(id, newStatus, ""); err != nil {
		if IsCandidateNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "candidate not found")
			return
		}
		if isInvalidTransition(err) {
			writeError(w, http.StatusConflict, "invalid_transition", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to update candidate")
		return
	}

	c, err := h.store.Get(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to reload candidate")
		return
	}

	writeJSON(w, http.StatusOK, c)
}

// HandleDiscoveryReport processes an incoming discovery_report WebSocket message
// from a probe and persists new candidates to the store.
func (h *CandidateHandler) HandleDiscoveryReport(probeID string, payload protocol.DiscoveryReportPayload) error {
	if h.store == nil {
		return nil
	}

	for _, host := range payload.Hosts {
		c := &DeployCandidate{
			SourceProbe: probeID,
			IP:          host.IP,
			Port:        host.Port,
			SSHBanner:   host.SSHBanner,
			OSGuess:     host.OSGuess,
			Fingerprint: host.Fingerprint,
			ReportedAt:  payload.ScannedAt,
		}
		if c.Port == 0 {
			c.Port = 22
		}
		if _, err := h.store.Upsert(c); err != nil {
			return err
		}
	}
	return nil
}

// HandleDeployResult processes an incoming deploy_result WebSocket message from
// a probe after it attempted remote installation.
func (h *CandidateHandler) HandleDeployResult(probeID string, payload protocol.DeployResultPayload) error {
	if h.store == nil {
		return nil
	}
	if payload.CandidateID == "" {
		return nil
	}

	newStatus := CandidateStatusDeployed
	errMsg := ""
	if !payload.Success {
		newStatus = CandidateStatusFailed
		errMsg = payload.Error
	}

	return h.store.Transition(payload.CandidateID, newStatus, errMsg)
}

// isInvalidTransition reports whether err wraps ErrInvalidTransition.
func isInvalidTransition(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), ErrInvalidTransition.Error())
}

// marshalPayload re-encodes env.Payload as JSON and decodes into dst.
func marshalPayload(raw any, dst any) error {
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
