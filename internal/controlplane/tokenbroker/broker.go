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

const (
	defaultTokenTTL  = 2 * time.Minute
	defaultMaxScope  = 8
	EventTokenIssued = "token.issued"
	EventTokenUsed   = "token.consumed"
	EventTokenExpire = "token.expired"
	EventTokenReject = "token.rejected"
)

var (
	ErrTokenRequired      = errors.New("token is required")
	ErrTokenInvalid       = errors.New("token is invalid")
	ErrTokenExpired       = errors.New("token is expired")
	ErrTokenRevoked       = errors.New("token is revoked")
	ErrTokenConsumed      = errors.New("token already consumed")
	ErrScopeRequired      = errors.New("scope is required")
	ErrScopeRejected      = errors.New("scope rejected")
	ErrRunIDRequired      = errors.New("run_id is required")
	ErrProbeIDRequired    = errors.New("probe_id is required")
	ErrIssuerRequired     = errors.New("issuer is required")
	ErrSessionMismatch    = errors.New("session binding rejected")
	ErrScopesRequired     = errors.New("scopes are required")
	ErrAudienceRequired   = errors.New("audience is required")
	ErrAudienceMismatch   = errors.New("audience mismatch")
	ErrBindingMismatch    = errors.New("binding mismatch")
	ErrScopeLimitExceeded = errors.New("scope exceeds configured max")
)

// Claims captures least-privilege token grants for a single runner operation flow.
type Claims struct {
	RunID     string    `json:"run_id"`
	ProbeID   string    `json:"probe_id"`
	Audience  string    `json:"audience"`
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
	Audience  string
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
	Audience  string
	RunID     string
	ProbeID   string
	SessionID string
	Consume   bool
}

// Event is emitted by the broker on token lifecycle transitions.
type Event struct {
	Type      string
	Token     string
	RunID     string
	ProbeID   string
	Audience  string
	Scopes    []string
	Issuer    string
	SessionID string
	Reason    string
	IssuedAt  time.Time
	ExpiresAt time.Time
	UsedAt    time.Time
}

// Config controls broker behaviour.
type Config struct {
	DefaultTTL     time.Duration
	MaxScope       int
	Now            func() time.Time
	TokenGenerator func() (string, error)
	Store          *Store
	AuditSink      func(Event)
}

// Broker mints and validates short-lived least-privilege operation tokens.
type Broker struct {
	mu             sync.Mutex
	store          *Store
	revoked        map[string]time.Time
	defaultTTL     time.Duration
	maxScope       int
	now            func() time.Time
	tokenGenerator func() (string, error)
	auditSink      func(Event)
}

// NewBroker creates a scoped token broker.
func NewBroker(cfg Config) *Broker {
	ttl := cfg.DefaultTTL
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	maxScope := cfg.MaxScope
	if maxScope <= 0 {
		maxScope = defaultMaxScope
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	tokenGen := cfg.TokenGenerator
	if tokenGen == nil {
		tokenGen = generateToken
	}

	store := cfg.Store
	if store == nil {
		var err error
		store, err = NewStore(":memory:")
		if err != nil {
			panic(fmt.Sprintf("token broker init failed: %v", err))
		}
	}

	return &Broker{
		store:          store,
		revoked:        make(map[string]time.Time),
		defaultTTL:     ttl,
		maxScope:       maxScope,
		now:            nowFn,
		tokenGenerator: tokenGen,
		auditSink:      cfg.AuditSink,
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
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, ErrSessionMismatch
	}
	audience := strings.TrimSpace(req.Audience)
	if audience == "" {
		return nil, ErrAudienceRequired
	}
	scopes := normalizeScopes(req.Scopes)
	if len(scopes) == 0 {
		return nil, ErrScopesRequired
	}
	if len(scopes) > b.maxScope {
		return nil, fmt.Errorf("%w: got %d > max %d", ErrScopeLimitExceeded, len(scopes), b.maxScope)
	}
	if !hasScope(scopes, audience) {
		return nil, fmt.Errorf("%w: expected scope %q", ErrScopeRejected, audience)
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = b.defaultTTL
	}
	issuedAt := b.now()
	expiresAt := issuedAt.Add(ttl)

	b.mu.Lock()
	defer b.mu.Unlock()

	const maxAttempts = 5
	var token string
	for i := 0; i < maxAttempts; i++ {
		generated, err := b.tokenGenerator()
		if err != nil {
			return nil, fmt.Errorf("generate scoped token: %w", err)
		}
		token = strings.TrimSpace(generated)
		if token == "" {
			return nil, fmt.Errorf("generate scoped token: empty token")
		}

		err = b.store.Insert(&TokenState{
			Token:     token,
			Scope:     scopes,
			Audience:  audience,
			RunnerID:  probeID,
			JobID:     runID,
			Issuer:    issuer,
			SessionID: strings.TrimSpace(req.SessionID),
			IssuedAt:  issuedAt,
			ExpiresAt: expiresAt,
		})
		if err == nil {
			issued := &IssuedToken{Token: token, Claims: Claims{
				RunID:     runID,
				ProbeID:   probeID,
				Audience:  audience,
				Scopes:    append([]string(nil), scopes...),
				IssuedAt:  issuedAt,
				ExpiresAt: expiresAt,
				Issuer:    issuer,
				SessionID: strings.TrimSpace(req.SessionID),
			}}
			b.emit(Event{
				Type:      EventTokenIssued,
				Token:     issued.Token,
				RunID:     issued.RunID,
				ProbeID:   issued.ProbeID,
				Audience:  issued.Audience,
				Scopes:    append([]string(nil), issued.Scopes...),
				Issuer:    issued.Issuer,
				SessionID: issued.SessionID,
				IssuedAt:  issued.IssuedAt,
				ExpiresAt: issued.ExpiresAt,
			})
			return issued, nil
		}
		if !errors.Is(err, ErrStoreTokenExists) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("generate scoped token: collision retries exhausted")
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

	state, err := b.store.Get(token)
	if err != nil {
		if errors.Is(err, ErrStoreTokenNotFound) {
			b.emitReject(req, nil, "invalid_token")
			return nil, ErrTokenInvalid
		}
		return nil, err
	}

	if revokedAt, ok := b.revoked[token]; ok {
		b.emit(Event{
			Type:      EventTokenReject,
			Token:     token,
			RunID:     state.JobID,
			ProbeID:   state.RunnerID,
			Audience:  state.Audience,
			Scopes:    append([]string(nil), state.Scope...),
			Issuer:    state.Issuer,
			SessionID: state.SessionID,
			Reason:    "revoked",
			IssuedAt:  state.IssuedAt,
			ExpiresAt: state.ExpiresAt,
			UsedAt:    revokedAt,
		})
		return nil, ErrTokenRevoked
	}

	now := b.now()
	if state.ConsumedAt != nil && req.Consume {
		b.emitReject(req, state, "token_consumed")
		return nil, ErrTokenConsumed
	}
	if now.After(state.ExpiresAt) {
		_, _ = b.store.MarkConsumed(token, now)
		b.emit(Event{
			Type:      EventTokenExpire,
			Token:     token,
			RunID:     state.JobID,
			ProbeID:   state.RunnerID,
			Audience:  state.Audience,
			Scopes:    append([]string(nil), state.Scope...),
			Issuer:    state.Issuer,
			SessionID: state.SessionID,
			Reason:    "expired",
			IssuedAt:  state.IssuedAt,
			ExpiresAt: state.ExpiresAt,
			UsedAt:    now,
		})
		return nil, ErrTokenExpired
	}

	if !hasScope(state.Scope, scope) {
		b.emitReject(req, state, "scope_mismatch")
		return nil, fmt.Errorf("%w: expected scope %q", ErrScopeRejected, scope)
	}
	if audience := strings.TrimSpace(req.Audience); audience != "" && audience != strings.TrimSpace(state.Audience) {
		b.emitReject(req, state, "audience_mismatch")
		return nil, fmt.Errorf("%w: expected audience %q", ErrAudienceMismatch, strings.TrimSpace(state.Audience))
	}
	if runID := strings.TrimSpace(req.RunID); runID != "" && runID != strings.TrimSpace(state.JobID) {
		b.emitReject(req, state, "run_binding_mismatch")
		return nil, fmt.Errorf("%w: token bound to run_id %q", ErrBindingMismatch, state.JobID)
	}
	if probeID := strings.TrimSpace(req.ProbeID); probeID != "" && probeID != strings.TrimSpace(state.RunnerID) {
		b.emitReject(req, state, "probe_binding_mismatch")
		return nil, fmt.Errorf("%w: token bound to probe_id %q", ErrBindingMismatch, state.RunnerID)
	}
	if sessionID := strings.TrimSpace(req.SessionID); strings.TrimSpace(state.SessionID) != "" && strings.TrimSpace(state.SessionID) != sessionID {
		b.emitReject(req, state, "session_mismatch")
		return nil, ErrSessionMismatch
	}

	if req.Consume {
		updated, err := b.store.MarkConsumed(token, now)
		if err != nil {
			return nil, err
		}
		if !updated {
			b.emitReject(req, state, "token_consumed")
			return nil, ErrTokenConsumed
		}
		b.emit(Event{
			Type:      EventTokenUsed,
			Token:     token,
			RunID:     state.JobID,
			ProbeID:   state.RunnerID,
			Audience:  state.Audience,
			Scopes:    append([]string(nil), state.Scope...),
			Issuer:    state.Issuer,
			SessionID: state.SessionID,
			IssuedAt:  state.IssuedAt,
			ExpiresAt: state.ExpiresAt,
			UsedAt:    now,
		})
	}

	claims := Claims{
		RunID:     state.JobID,
		ProbeID:   state.RunnerID,
		Audience:  state.Audience,
		Scopes:    append([]string(nil), state.Scope...),
		IssuedAt:  state.IssuedAt,
		ExpiresAt: state.ExpiresAt,
		Issuer:    state.Issuer,
		SessionID: state.SessionID,
	}
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

	state, err := b.store.Get(token)
	if err != nil {
		if errors.Is(err, ErrStoreTokenNotFound) {
			return ErrTokenInvalid
		}
		return err
	}
	now := b.now()
	b.revoked[token] = now
	_, _ = b.store.MarkConsumed(token, now)
	b.emit(Event{
		Type:      EventTokenReject,
		Token:     token,
		RunID:     state.JobID,
		ProbeID:   state.RunnerID,
		Audience:  state.Audience,
		Scopes:    append([]string(nil), state.Scope...),
		Issuer:    state.Issuer,
		SessionID: state.SessionID,
		Reason:    "revoked",
		IssuedAt:  state.IssuedAt,
		ExpiresAt: state.ExpiresAt,
		UsedAt:    now,
	})
	return nil
}

// Close closes the underlying persistence store.
func (b *Broker) Close() error {
	if b == nil || b.store == nil {
		return nil
	}
	return b.store.Close()
}

func (b *Broker) emit(evt Event) {
	if b.auditSink == nil {
		return
	}
	b.auditSink(evt)
}

func (b *Broker) emitReject(req ValidateRequest, state *TokenState, reason string) {
	evt := Event{
		Type:      EventTokenReject,
		Token:     strings.TrimSpace(req.Token),
		RunID:     strings.TrimSpace(req.RunID),
		ProbeID:   strings.TrimSpace(req.ProbeID),
		Audience:  strings.TrimSpace(req.Audience),
		SessionID: strings.TrimSpace(req.SessionID),
		Reason:    strings.TrimSpace(reason),
	}
	if state != nil {
		evt.Token = state.Token
		evt.RunID = state.JobID
		evt.ProbeID = state.RunnerID
		evt.Audience = state.Audience
		evt.Scopes = append([]string(nil), state.Scope...)
		evt.Issuer = state.Issuer
		evt.SessionID = state.SessionID
		evt.IssuedAt = state.IssuedAt
		evt.ExpiresAt = state.ExpiresAt
		if state.ConsumedAt != nil {
			evt.UsedAt = state.ConsumedAt.UTC()
		}
	}
	b.emit(evt)
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
