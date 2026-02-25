// Package api implements the control plane's HTTP API handlers.
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

// Token represents a single-use registration token.
type Token struct {
	Value   string    `json:"token"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Used    bool      `json:"used"`
}

// TokenStore manages registration tokens.
type TokenStore struct {
	tokens map[string]*Token
	secret []byte
	mu     sync.RWMutex
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
	ts.tokens[token.Value] = token
	return token
}

// Consume validates and consumes a token. Returns false if invalid/expired/used.
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

// RegisterRequest is the probe's registration request.
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

// HandleRegister returns an HTTP handler for probe registration.
func HandleRegister(ts *TokenStore, fm *fleet.Manager, logger *zap.Logger) http.HandlerFunc {
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
		apiKey := generateAPIKey()

		// Register in fleet
		fm.Register(probeID, req.Hostname, req.OS, req.Arch)

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
		// TODO: require authentication
		token := ts.Generate()
		logger.Info("token generated", zap.String("expires", token.Expires.Format(time.RFC3339)))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(token)
	}
}

func generateAPIKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "lgk_" + hex.EncodeToString([]byte("fallback-key"))
	}
	return "lgk_" + hex.EncodeToString(b)
}

// HandleRegisterWithAudit wraps HandleRegister with audit logging.
func HandleRegisterWithAudit(ts *TokenStore, fm *fleet.Manager, al *audit.Log, logger *zap.Logger) http.HandlerFunc {
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
		apiKey := generateAPIKey()

		fm.Register(probeID, req.Hostname, req.OS, req.Arch)

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
func HandleGenerateTokenWithAudit(ts *TokenStore, al *audit.Log, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ts.Generate()
		al.Emit(audit.EventTokenGenerated, "", "api", "Registration token generated")
		logger.Info("token generated", zap.String("expires", token.Expires.Format("2006-01-02T15:04:05Z")))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(token)
	}
}
