// Package api implements the control plane HTTP API handlers.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"go.uber.org/zap"
)

// AuditRecorder is satisfied by both *audit.Log and *audit.Store.
type AuditRecorder interface {
	Record(evt audit.Event)
	Emit(typ audit.EventType, probeID, actor, summary string)
}

func installCommand(serverURL, token string) string {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" || token == "" {
		return ""
	}
	return fmt.Sprintf(
		"curl -sSL %s/install.sh | sudo bash -s -- --server %s --token %s",
		serverURL, serverURL, token,
	)
}

func requestBaseURL(r *http.Request) string {
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

func tokenWithInstallCommand(token *Token, fallbackBaseURL string) Token {
	if token == nil {
		return Token{}
	}
	copy := *token
	if copy.InstallCommand == "" {
		copy.InstallCommand = installCommand(fallbackBaseURL, copy.Value)
	}
	return copy
}

// RegisterRequest is the probe registration request.
type RegisterRequest struct {
	Token    string   `json:"token"`
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Version  string   `json:"version"`
	Tags     []string `json:"tags,omitempty"`
}

// RegisterResponse is returned on successful registration.
type RegisterResponse struct {
	ProbeID  string `json:"probe_id"`
	APIKey   string `json:"api_key"`
	PolicyID string `json:"policy_id"`
}

// GenerateAPIKey creates a 32-byte cryptographically secure API key and returns it as hex with lgk_ prefix.
func GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return "lgk_" + hex.EncodeToString(b), nil
}

type registerProbeResult struct {
	probeID      string
	apiKey       string
	reRegistered bool
}

func registerProbe(fm fleet.Fleet, req RegisterRequest) (*registerProbeResult, error) {
	probeID := "prb-" + uuid.New().String()[:8]
	reRegistered := false
	if existing, ok := fm.FindByHostname(req.Hostname); ok {
		probeID = existing.ID
		reRegistered = true
	}

	apiKey, err := GenerateAPIKey()
	if err != nil {
		return nil, err
	}

	fm.Register(probeID, req.Hostname, req.OS, req.Arch)
	_ = fm.SetAPIKey(probeID, apiKey)
	_ = fm.SetTags(probeID, req.Tags)

	return &registerProbeResult{
		probeID:      probeID,
		apiKey:       apiKey,
		reRegistered: reRegistered,
	}, nil
}

// HandleRegister returns an HTTP handler for probe registration.
func HandleRegister(ts *TokenStore, fm fleet.Fleet, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}

		if !ts.Consume(req.Token) {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		result, err := registerProbe(fm, req)
		if err != nil {
			http.Error(w, `{"error":"failed to generate api key"}`, http.StatusInternalServerError)
			return
		}

		if result.reRegistered {
			logger.Info("probe re-registered",
				zap.String("probe_id", result.probeID),
				zap.String("hostname", req.Hostname),
				zap.String("os", req.OS),
				zap.String("arch", req.Arch),
			)
		} else {
			logger.Info("probe registered",
				zap.String("probe_id", result.probeID),
				zap.String("hostname", req.Hostname),
				zap.String("os", req.OS),
				zap.String("arch", req.Arch),
			)
		}

		resp := RegisterResponse{
			ProbeID:  result.probeID,
			APIKey:   result.apiKey,
			PolicyID: "default-observe",
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// HandleGenerateToken returns an HTTP handler for creating registration tokens.
func HandleGenerateToken(ts *TokenStore, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		multiUse := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("multi_use")), "true")
		noExpiry := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("no_expiry")), "true")
		token := ts.GenerateWithOptions(GenerateOptions{MultiUse: multiUse, NoExpiry: noExpiry})
		out := tokenWithInstallCommand(token, requestBaseURL(r))
		logger.Info("token generated",
			zap.String("expires", out.Expires.Format(time.RFC3339)),
			zap.Bool("multi_use", out.MultiUse),
			zap.Bool("no_expiry", noExpiry),
		)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// HandleListTokens returns an HTTP handler that lists active (unused, unexpired) tokens.
func HandleListTokens(ts *TokenStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		active := ts.ListActive()
		baseURL := requestBaseURL(r)
		tokens := make([]Token, 0, len(active))
		for _, t := range active {
			tokens = append(tokens, tokenWithInstallCommand(t, baseURL))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tokens": tokens,
			"count":  len(tokens),
			"total":  ts.Count(),
		})
	}
}

// HandleRegisterWithAudit wraps HandleRegister with audit logging.
func HandleRegisterWithAudit(ts *TokenStore, fm fleet.Fleet, al AuditRecorder, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}

		if !ts.Consume(req.Token) {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		result, err := registerProbe(fm, req)
		if err != nil {
			http.Error(w, `{"error":"failed to generate api key"}`, http.StatusInternalServerError)
			return
		}

		summary := "Probe registered: " + req.Hostname
		if result.reRegistered {
			summary = "Probe re-registered: " + req.Hostname
		}

		al.Record(audit.Event{
			Type:    audit.EventProbeRegistered,
			ProbeID: result.probeID,
			Actor:   "system",
			Summary: summary,
			Detail:  map[string]string{"os": req.OS, "arch": req.Arch, "hostname": req.Hostname},
		})

		if result.reRegistered {
			logger.Info("probe re-registered",
				zap.String("probe_id", result.probeID),
				zap.String("hostname", req.Hostname),
			)
		} else {
			logger.Info("probe registered",
				zap.String("probe_id", result.probeID),
				zap.String("hostname", req.Hostname),
			)
		}

		resp := RegisterResponse{ProbeID: result.probeID, APIKey: result.apiKey, PolicyID: "default-observe"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// HandleGenerateTokenWithAudit wraps HandleGenerateToken with audit logging.
func HandleGenerateTokenWithAudit(ts *TokenStore, al AuditRecorder, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		multiUse := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("multi_use")), "true")
		noExpiry := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("no_expiry")), "true")
		token := ts.GenerateWithOptions(GenerateOptions{MultiUse: multiUse, NoExpiry: noExpiry})
		out := tokenWithInstallCommand(token, requestBaseURL(r))
		al.Emit(audit.EventTokenGenerated, "", "api", "Registration token generated")
		logger.Info("token generated",
			zap.String("expires", out.Expires.Format("2006-01-02T15:04:05Z")),
			zap.Bool("multi_use", out.MultiUse),
		)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}
