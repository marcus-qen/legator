// Package api implements the control plane HTTP API handlers.
package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

// Token represents a registration token.
type Token struct {
	Value          string    `json:"token"`
	Created        time.Time `json:"created"`
	Expires        time.Time `json:"expires"`
	Used           bool      `json:"used"`
	MultiUse       bool      `json:"multi_use,omitempty"`
	InstallCommand string    `json:"install_command,omitempty"`
}

// TokenStore manages registration tokens.
type TokenStore struct {
	tokens    map[string]*Token
	secret    []byte
	serverURL string // used to generate install commands
	mu        sync.RWMutex
}

// NewTokenStore creates a token store with a random HMAC secret.
func NewTokenStore() *TokenStore {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return &TokenStore{
		tokens: make(map[string]*Token),
		secret: secret,
	}
}

// SetServerURL sets the server URL used in install commands.
func (ts *TokenStore) SetServerURL(url string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.serverURL = strings.TrimRight(strings.TrimSpace(url), "/")
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

// GenerateOptions controls token generation behavior.
type GenerateOptions struct {
	MultiUse bool
	NoExpiry bool
}

// Generate creates a new registration token valid for 30 minutes.
func (ts *TokenStore) Generate() *Token {
	return ts.GenerateWithOptions(GenerateOptions{})
}

// GenerateWithOptions creates a new registration token using options.
func (ts *TokenStore) GenerateWithOptions(opts GenerateOptions) *Token {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	now := time.Now().UTC()
	id := uuid.New().String()[:12]

	mac := hmac.New(sha256.New, ts.secret)
	mac.Write([]byte(id))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]

	expiry := now.Add(30 * time.Minute)
	if opts.NoExpiry {
		expiry = now.Add(100 * 365 * 24 * time.Hour)
	}

	token := &Token{
		Value:    fmt.Sprintf("prb_%s_%d_%s", id, now.Unix(), sig),
		Created:  now,
		Expires:  expiry,
		MultiUse: opts.MultiUse,
	}

	if ts.serverURL != "" {
		token.InstallCommand = installCommand(ts.serverURL, token.Value)
	}

	ts.tokens[token.Value] = token
	return token
}

// Consume validates and consumes a token. Returns false if invalid, expired, or already used.
func (ts *TokenStore) Consume(value string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	t, ok := ts.tokens[value]
	if !ok {
		return false
	}
	if time.Now().UTC().After(t.Expires) {
		return false
	}
	if t.Used {
		return false
	}
	if !t.MultiUse {
		t.Used = true
	}
	return true
}

// ListActive returns all tokens that are still valid for registration.
func (ts *TokenStore) ListActive() []*Token {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	now := time.Now().UTC()
	var active []*Token
	for _, t := range ts.tokens {
		if !t.Used && now.Before(t.Expires) {
			active = append(active, t)
		}
	}
	return active
}

// Count returns the total number of tokens (active + used + expired).
func (ts *TokenStore) Count() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.tokens)
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

		// Generate probe identity
		probeID := "prb-" + uuid.New().String()[:8]
		apiKey, err := GenerateAPIKey()
		if err != nil {
			http.Error(w, `{"error":"failed to generate api key"}`, http.StatusInternalServerError)
			return
		}

		// Register in fleet
		fm.Register(probeID, req.Hostname, req.OS, req.Arch)
		_ = fm.SetAPIKey(probeID, apiKey)
		if len(req.Tags) > 0 {
			_ = fm.SetTags(probeID, req.Tags)
		}

		logger.Info("probe registered",
			zap.String("probe_id", probeID),
			zap.String("hostname", req.Hostname),
			zap.String("os", req.OS),
			zap.String("arch", req.Arch),
		)

		resp := RegisterResponse{
			ProbeID:  probeID,
			APIKey:   apiKey,
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

		probeID := "prb-" + uuid.New().String()[:8]
		apiKey, err := GenerateAPIKey()
		if err != nil {
			http.Error(w, `{"error":"failed to generate api key"}`, http.StatusInternalServerError)
			return
		}

		fm.Register(probeID, req.Hostname, req.OS, req.Arch)
		_ = fm.SetAPIKey(probeID, apiKey)
		if len(req.Tags) > 0 {
			_ = fm.SetTags(probeID, req.Tags)
		}

		al.Record(audit.Event{
			Type:    audit.EventProbeRegistered,
			ProbeID: probeID,
			Actor:   "system",
			Summary: "Probe registered: " + req.Hostname,
			Detail:  map[string]string{"os": req.OS, "arch": req.Arch, "hostname": req.Hostname},
		})

		logger.Info("probe registered",
			zap.String("probe_id", probeID),
			zap.String("hostname", req.Hostname),
		)

		resp := RegisterResponse{ProbeID: probeID, APIKey: apiKey, PolicyID: "default-observe"}
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
