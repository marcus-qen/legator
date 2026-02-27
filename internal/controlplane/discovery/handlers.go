package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/api"
)

// ScannerAPI is the scanner behavior required by handlers.
type ScannerAPI interface {
	Scan(ctx context.Context, cidr string, hostTimeout time.Duration) ([]Candidate, error)
}

// Handler serves discovery APIs.
type Handler struct {
	store      *Store
	scanner    ScannerAPI
	tokenStore *api.TokenStore
}

func NewHandler(store *Store, scanner ScannerAPI, tokenStore *api.TokenStore) *Handler {
	if scanner == nil {
		scanner = NewScanner()
	}
	return &Handler{store: store, scanner: scanner, tokenStore: tokenStore}
}

type scanRequest struct {
	CIDR      string `json:"cidr"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

func (h *Handler) HandleScan(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "discovery store unavailable")
		return
	}

	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	req.CIDR = strings.TrimSpace(req.CIDR)
	if _, err := ValidateCIDR(req.CIDR); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cidr", err.Error())
		return
	}

	run, err := h.store.CreateRun(req.CIDR, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to persist scan run")
		return
	}

	hostTimeout := NormalizeHostTimeout(time.Duration(req.TimeoutMS) * time.Millisecond)
	candidates, scanErr := h.scanner.Scan(r.Context(), req.CIDR, hostTimeout)
	if scanErr != nil {
		_ = h.store.CompleteRun(run.ID, StatusFailed, scanErr.Error(), time.Now().UTC())
		writeError(w, http.StatusBadGateway, "scan_failed", scanErr.Error())
		return
	}

	if err := h.store.ReplaceCandidates(run.ID, candidates); err != nil {
		_ = h.store.CompleteRun(run.ID, StatusFailed, err.Error(), time.Now().UTC())
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to persist scan candidates")
		return
	}

	if err := h.store.CompleteRun(run.ID, StatusCompleted, "", time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to finalize scan run")
		return
	}

	resp, err := h.store.GetRunWithCandidates(run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load scan result")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) HandleListRuns(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "discovery store unavailable")
		return
	}

	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = value
	}

	runs, err := h.store.ListRuns(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list runs")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *Handler) HandleGetRun(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "discovery store unavailable")
		return
	}

	rawID := strings.TrimSpace(r.PathValue("id"))
	runID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || runID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "run id must be a positive integer")
		return
	}

	resp, err := h.store.GetRunWithCandidates(runID)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load run")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) HandleInstallToken(w http.ResponseWriter, r *http.Request) {
	if h.tokenStore == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "token store unavailable")
		return
	}

	token := h.tokenStore.GenerateWithOptions(api.GenerateOptions{MultiUse: true})
	serverURL := baseURLFromRequest(r)
	installCommand := token.InstallCommand
	if installCommand == "" {
		if serverURL == "" {
			serverURL = "<server>"
		}
		installCommand = buildInstallCommand(serverURL, token.Value)
	}

	sshTemplate := fmt.Sprintf("ssh <user>@<ip> %q", installCommand)

	response := InstallTokenResponse{
		Token:              token.Value,
		ExpiresAt:          token.Expires,
		InstallCommand:     installCommand,
		SSHExampleTemplate: sshTemplate,
	}

	writeJSON(w, http.StatusOK, response)
}

func buildInstallCommand(serverURL, token string) string {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	token = strings.TrimSpace(token)
	if serverURL == "" || token == "" {
		return ""
	}
	return fmt.Sprintf("curl -sSL %s/install.sh | sudo bash -s -- --server %s --token %s", serverURL, serverURL, token)
}

func baseURLFromRequest(r *http.Request) string {
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}

	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + host
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
