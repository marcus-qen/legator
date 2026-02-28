package grafana

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Handler exposes Grafana read-only APIs.
type Handler struct {
	client Client
}

func NewHandler(client Client) *Handler {
	return &Handler{client: client}
}

func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.client.Status(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (h *Handler) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.client.Snapshot(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": snapshot})
}

func writeClientError(w http.ResponseWriter, err error) {
	if err == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected grafana error")
		return
	}

	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		writeError(w, http.StatusBadGateway, "grafana_error", err.Error())
		return
	}

	switch clientErr.Code {
	case "config_invalid":
		writeError(w, http.StatusServiceUnavailable, clientErr.Code, clientErr.Message)
	case "auth_failed", "timeout", "request_failed", "parse_error", "unreachable":
		writeError(w, http.StatusBadGateway, clientErr.Code, clientErr.Error())
	default:
		writeError(w, http.StatusBadGateway, "grafana_error", clientErr.Error())
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
