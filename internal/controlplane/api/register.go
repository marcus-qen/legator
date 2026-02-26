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

// Token represents a single-use registration token.
type Token struct {
	Value          string    `json:"token"`
	Created        time.Time `json:"created"`
	Expires        time.Time `json:"expires"`
	Used           bool      `json:"used"`
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
	ts.serverURL = url
}

// Generate creates a new registration token valid for 30 minutes.
func (ts *TokenStore) Generate() *Token {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	now := time.Now().UTC()
	id := uuid.New().String()[:12]

	mac := hmac.New(sha256.New, ts.secret)
	mac.Write([]byte(id))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]

	token := &Token{
		Value:   fmt.Sprintf("prb_%s_%d_%s", id, now.Unix(), sig),
		Created: now,
		Expires: now.Add(30 * time.Minute),
	}

	if ts.serverURL != "" {
		token.InstallCommand = fmt.Sprintf(
			"curl -sSL %s/install.sh | sudo bash -s -- --server %s --token %s",
			ts.serverURL, ts.serverURL, token.Value,
		)
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
	if t.Used || time.Now().UTC().After(t.Expires) {
		return false
	}
	t.Used = true
	return true
}

// ListActive returns all tokens that are unexpired and unused.
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
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
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
		token := ts.Generate()
		logger.Info("token generated", zap.String("expires", token.Expires.Format(time.RFC3339)))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(token)
	}
}

// HandleListTokens returns an HTTP handler that lists active (unused, unexpired) tokens.
func HandleListTokens(ts *TokenStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		active := ts.ListActive()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tokens": active,
			"count":  len(active),
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
		token := ts.Generate()
		al.Emit(audit.EventTokenGenerated, "", "api", "Registration token generated")
		logger.Info("token generated", zap.String("expires", token.Expires.Format("2006-01-02T15:04:05Z")))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(token)
	}
}
