package kubeflow

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Handler exposes Kubeflow read-only APIs and optional guarded actions.
type Handler struct {
	client         Client
	actionsEnabled bool
}

type HandlerOptions struct {
	ActionsEnabled bool
}

func NewHandler(client Client, opts HandlerOptions) *Handler {
	return &Handler{client: client, actionsEnabled: opts.ActionsEnabled}
}

func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.client.Status(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (h *Handler) HandleInventory(w http.ResponseWriter, r *http.Request) {
	inventory, err := h.client.Inventory(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inventory": inventory})
}

func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	if !h.actionsEnabled {
		writeError(w, http.StatusForbidden, "action_disabled", "kubeflow actions are disabled by policy")
		return
	}

	result, err := h.client.Refresh(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"refresh": result})
}

func writeClientError(w http.ResponseWriter, err error) {
	if err == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected kubeflow error")
		return
	}

	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		writeError(w, http.StatusBadGateway, "kubeflow_error", err.Error())
		return
	}

	switch clientErr.Code {
	case "cli_missing":
		writeError(w, http.StatusServiceUnavailable, clientErr.Code, clientErr.Message)
	case "namespace_missing":
		writeError(w, http.StatusNotFound, clientErr.Code, clientErr.Message)
	case "auth_failed", "cluster_unreachable", "timeout", "inventory_unavailable", "command_failed", "parse_error":
		writeError(w, http.StatusBadGateway, clientErr.Code, clientErr.Error())
	default:
		writeError(w, http.StatusBadGateway, "kubeflow_error", clientErr.Error())
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
