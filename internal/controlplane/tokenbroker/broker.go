package tokenbroker

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultTokenTTL = 2 * time.Minute

var (
	ErrTokenRequired   = errors.New("token is required")
	ErrTokenInvalid    = errors.New("token is invalid")
	ErrTokenExpired    = errors.New("token is expired")
	ErrTokenRevoked    = errors.New("token is revoked")
	ErrTokenConsumed   = errors.New("token already consumed")
	ErrScopeRequired   = errors.New("scope is required")
	ErrScopeRejected   = errors.New("scope rejected")
	ErrRunIDRequired   = errors.New("run_id is required")
	ErrProbeIDRequired = errors.New("probe_id is required")
	ErrIssuerRequired  = errors.New("issuer is required")
	ErrSessionMismatch = errors.New("session binding rejected")
	ErrScopesRequired  = errors.New("scopes are required")
)

// Claims captures least-privilege token grants for a single runner operation flow.
type Claims struct {
	RunID     string    `json:"run_id"`
	ProbeID   string    `json:"probe_id"`
	Scopes    []string  `json:"scopes"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Issuer    string    `json:"issuer"`
	SessionID string    `json:"session_id,omitempty"`
}

// IssueRequest creates a new short-lived scoped token.
type IssueRequest struct {
	RunID     string
	ProbeID   string
	Scopes    []string
	Issuer    string
	SessionID string
	TTL       time.Duration
}

// IssuedToken is returned after successful issuance.
type IssuedToken struct {
	Token string `json:"token"`
	Claims
}

// ValidateRequest checks a token against operation scope + context.
type ValidateRequest struct {
	Token     string
	Scope     string
	RunID     string
	ProbeID   string
	SessionID string
	Consume   bool
}

// Config controls broker behaviour.
type Config struct {
	DefaultTTL     time.Duration
	Now            func() time.Time
	TokenGenerator func() (string, error)
}

type tokenRecord struct {
	Token      string
	Claims     Claims
	ConsumedAt *time.Time
	RevokedAt  *time.Time
}

// Broker mints and validates short-lived least-privilege operation tokens.
type Broker struct {
	mu             sync.Mutex
	tokens         map[string]*tokenRecord
	defaultTTL     time.Duration
	now            func() time.Time
	tokenGenerator func() (string, error)
}

// NewBroker creates an in-memory scoped token broker.
func NewBroker(cfg Config) *Broker {
	ttl := cfg.DefaultTTL
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	tokenGen := cfg.TokenGenerator
	if tokenGen == nil {
		tokenGen = generateToken
	}

	return &Broker{
		tokens:         make(map[string]*tokenRecord),
		defaultTTL:     ttl,
		now:            nowFn,
		tokenGenerator: tokenGen,
	}
}

// Issue mints a scoped, short-lived token.
func (b *Broker) Issue(req IssueRequest) (*IssuedToken, error) {
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		return nil, ErrRunIDRequired
	}
	probeID := strings.TrimSpace(req.ProbeID)
	if probeID == "" {
		return nil, ErrProbeIDRequired
	}
	issuer := strings.TrimSpace(req.Issuer)
	if issuer == "" {
		return nil, ErrIssuerRequired
	}
	scopes := normalizeScopes(req.Scopes)
	if len(scopes) == 0 {
		return nil, ErrScopesRequired
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = b.defaultTTL
	}
	issuedAt := b.now()
	expiresAt := issuedAt.Add(ttl)

	token, err := b.tokenGenerator()
	if err != nil {
		return nil, fmt.Errorf("generate scoped token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("generate scoped token: empty token")
	}

	rec := &tokenRecord{
		Token: token,
		Claims: Claims{
			RunID:     runID,
			ProbeID:   probeID,
			Scopes:    scopes,
			IssuedAt:  issuedAt,
			ExpiresAt: expiresAt,
			Issuer:    issuer,
			SessionID: strings.TrimSpace(req.SessionID),
		},
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked()
	b.tokens[token] = rec

	out := &IssuedToken{Token: token, Claims: cloneClaims(rec.Claims)}
	return out, nil
}

// Validate checks token TTL, revocation, scope and bindings.
func (b *Broker) Validate(req ValidateRequest) (*Claims, error) {
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return nil, ErrTokenRequired
	}
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		return nil, ErrScopeRequired
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked()

	rec, ok := b.tokens[token]
	if !ok || rec == nil {
		return nil, ErrTokenInvalid
	}

	now := b.now()
	if rec.RevokedAt != nil {
		return nil, ErrTokenRevoked
	}
	if now.After(rec.Claims.ExpiresAt) {
		if rec.ConsumedAt == nil {
			t := now
			rec.ConsumedAt = &t
		}
		return nil, ErrTokenExpired
	}
	if req.Consume && rec.ConsumedAt != nil {
		return nil, ErrTokenConsumed
	}

	if !hasScope(rec.Claims.Scopes, scope) {
		return nil, fmt.Errorf("%w: expected scope %q", ErrScopeRejected, scope)
	}

	runID := strings.TrimSpace(req.RunID)
	if runID != "" && runID != strings.TrimSpace(rec.Claims.RunID) {
		return nil, fmt.Errorf("%w: token bound to run_id %q", ErrScopeRejected, rec.Claims.RunID)
	}
	probeID := strings.TrimSpace(req.ProbeID)
	if probeID != "" && probeID != strings.TrimSpace(rec.Claims.ProbeID) {
		return nil, fmt.Errorf("%w: token bound to probe_id %q", ErrScopeRejected, rec.Claims.ProbeID)
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if strings.TrimSpace(rec.Claims.SessionID) != "" && strings.TrimSpace(rec.Claims.SessionID) != sessionID {
		return nil, ErrSessionMismatch
	}

	if req.Consume {
		t := now
		rec.ConsumedAt = &t
	}

	claims := cloneClaims(rec.Claims)
	return &claims, nil
}

// Revoke invalidates an issued token.
func (b *Broker) Revoke(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrTokenRequired
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.tokens[token]
	if !ok || rec == nil {
		return ErrTokenInvalid
	}
	now := b.now()
	rec.RevokedAt = &now
	return nil
}

func (b *Broker) pruneLocked() {
	now := b.now()
	for token, rec := range b.tokens {
		if rec == nil {
			delete(b.tokens, token)
			continue
		}
		if rec.RevokedAt != nil {
			if now.Sub(*rec.RevokedAt) > time.Hour {
				delete(b.tokens, token)
			}
			continue
		}
		if rec.ConsumedAt != nil {
			if now.Sub(*rec.ConsumedAt) > time.Hour {
				delete(b.tokens, token)
			}
			continue
		}
		if now.After(rec.Claims.ExpiresAt.Add(time.Hour)) {
			delete(b.tokens, token)
		}
	}
}

func cloneClaims(in Claims) Claims {
	out := in
	out.Scopes = append([]string(nil), in.Scopes...)
	return out
}

func normalizeScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		scope := strings.TrimSpace(raw)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func hasScope(scopes []string, scope string) bool {
	scope = strings.TrimSpace(scope)
	for _, granted := range scopes {
		if strings.TrimSpace(granted) == scope {
			return true
		}
	}
	return false
}

func generateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "lgrt_" + hex.EncodeToString(raw), nil
}
