package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/users"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

const (
	stateCookieName        = "legator_oidc_state"
	stateCookieTTL         = 5 * time.Minute
	oidcSessionMaxAge      = 86400
	randomEntropyByteCount = 32
)

// UserStore is the user persistence contract needed for OIDC login.
type UserStore interface {
	Get(id string) (*users.User, error)
	GetByUsername(username string) (*users.User, error)
	CreateWithID(id, username, displayName, password, role string) (*users.User, error)
	UpdateRole(id, role string) error
	UpdateProfile(id, username, displayName string) error
}

// SessionCreator is the session creation contract needed for OIDC login.
type SessionCreator interface {
	Create(userID string) (token string, err error)
}

// Provider handles OIDC login + callback processing.
type Provider struct {
	config   Config
	verifier *gooidc.IDTokenVerifier
	oauth2   oauth2.Config
	logger   *zap.Logger
}

type callbackState struct {
	State        string `json:"state"`
	Nonce        string `json:"nonce"`
	CodeVerifier string `json:"code_verifier"`
	ExpiresAt    int64  `json:"expires_at"`
}

// NewProvider builds an OIDC provider from config and OIDC discovery metadata.
func NewProvider(ctx context.Context, cfg Config, logger *zap.Logger) (*Provider, error) {
	cfg = cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, errors.New("oidc disabled")
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	discovery, err := gooidc.NewProvider(ctx, cfg.ProviderURL)
	if err != nil {
		return nil, fmt.Errorf("discover oidc provider: %w", err)
	}

	return &Provider{
		config: cfg,
		verifier: discovery.Verifier(&gooidc.Config{
			ClientID: cfg.ClientID,
		}),
		oauth2: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     discovery.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       append([]string{}, cfg.Scopes...),
		},
		logger: logger.Named("oidc"),
	}, nil
}

// Enabled returns true when provider is configured and active.
func (p *Provider) Enabled() bool {
	return p != nil && p.config.Enabled
}

// ProviderName returns display name used in login UI.
func (p *Provider) ProviderName() string {
	if p == nil {
		return "OIDC"
	}
	return p.config.EffectiveProviderName()
}

// HandleLogin starts the auth code flow and redirects user to OIDC provider.
func (p *Provider) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if p == nil || !p.config.Enabled {
		http.NotFound(w, r)
		return
	}

	state, err := randomToken(randomEntropyByteCount)
	if err != nil {
		http.Error(w, "failed to start oidc login", http.StatusInternalServerError)
		return
	}
	nonce, err := randomToken(randomEntropyByteCount)
	if err != nil {
		http.Error(w, "failed to start oidc login", http.StatusInternalServerError)
		return
	}
	codeVerifier, err := randomToken(randomEntropyByteCount)
	if err != nil {
		http.Error(w, "failed to start oidc login", http.StatusInternalServerError)
		return
	}

	payload := callbackState{
		State:        state,
		Nonce:        nonce,
		CodeVerifier: codeVerifier,
		ExpiresAt:    time.Now().Add(stateCookieTTL).Unix(),
	}
	encoded, err := encodeState(payload)
	if err != nil {
		http.Error(w, "failed to start oidc login", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    encoded,
		Path:     "/auth/oidc",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(stateCookieTTL.Seconds()),
		Expires:  time.Now().Add(stateCookieTTL),
	})

	authURL := p.oauth2.AuthCodeURL(state,
		oauth2.AccessTypeOnline,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge(codeVerifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleCallback finishes OIDC login, maps claims to user, and creates a session.
func (p *Provider) HandleCallback(userStore UserStore, sessionCreator SessionCreator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p == nil || !p.config.Enabled {
			http.NotFound(w, r)
			return
		}
		if userStore == nil || sessionCreator == nil {
			http.Error(w, "oidc login unavailable", http.StatusServiceUnavailable)
			return
		}

		stateCookie, err := r.Cookie(stateCookieName)
		if err != nil || strings.TrimSpace(stateCookie.Value) == "" {
			http.Error(w, "missing oidc state", http.StatusUnauthorized)
			return
		}
		stored, err := decodeState(stateCookie.Value)
		if err != nil {
			http.Error(w, "invalid oidc state", http.StatusUnauthorized)
			return
		}
		if time.Now().Unix() > stored.ExpiresAt {
			http.Error(w, "oidc state expired", http.StatusUnauthorized)
			return
		}
		if got := strings.TrimSpace(r.URL.Query().Get("state")); got == "" || got != stored.State {
			http.Error(w, "invalid oidc state", http.StatusUnauthorized)
			return
		}

		clearStateCookie(w)

		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			return
		}

		tok, err := p.oauth2.Exchange(r.Context(), code,
			oauth2.SetAuthURLParam("code_verifier", stored.CodeVerifier),
		)
		if err != nil {
			http.Error(w, "oidc token exchange failed", http.StatusUnauthorized)
			return
		}

		rawIDToken, _ := tok.Extra("id_token").(string)
		if strings.TrimSpace(rawIDToken) == "" {
			http.Error(w, "oidc provider did not return id_token", http.StatusUnauthorized)
			return
		}

		idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
		if err != nil {
			http.Error(w, "invalid oidc id_token", http.StatusUnauthorized)
			return
		}
		if strings.TrimSpace(idToken.Nonce) == "" || idToken.Nonce != stored.Nonce {
			http.Error(w, "invalid oidc nonce", http.StatusUnauthorized)
			return
		}

		claims := map[string]any{}
		if err := idToken.Claims(&claims); err != nil {
			http.Error(w, "invalid oidc claims", http.StatusUnauthorized)
			return
		}

		user, err := p.reconcileUser(r.Context(), userStore, claims)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		sessionToken, err := sessionCreator.Create(user.ID)
		if err != nil || strings.TrimSpace(sessionToken) == "" {
			http.Error(w, "failed to create session", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     auth.SessionCookieName,
			Value:    sessionToken,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   oidcSessionMaxAge,
			Expires:  time.Now().Add(oidcSessionMaxAge * time.Second),
		})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

func (p *Provider) reconcileUser(_ context.Context, store UserStore, claims map[string]any) (*users.User, error) {
	sub := strings.TrimSpace(claimString(claims, "sub"))
	if sub == "" {
		return nil, errors.New("oidc subject claim missing")
	}
	oidcUserID := "oidc:" + sub

	username := strings.TrimSpace(firstNonEmpty(
		claimString(claims, "preferred_username"),
		claimString(claims, "email"),
		sub,
	))
	if username == "" {
		return nil, errors.New("oidc username claim missing")
	}
	displayName := strings.TrimSpace(firstNonEmpty(
		claimString(claims, "name"),
		claimString(claims, "display_name"),
		username,
	))

	targetRole := p.resolveRole(claims)

	user, err := store.Get(oidcUserID)
	if err != nil {
		if !errors.Is(err, users.ErrUserNotFound) {
			return nil, fmt.Errorf("lookup oidc user: %w", err)
		}
		user, err = store.GetByUsername(username)
		if err != nil {
			if !errors.Is(err, users.ErrUserNotFound) {
				return nil, fmt.Errorf("lookup user by username: %w", err)
			}
			if !p.config.AutoCreateUsers {
				return nil, errors.New("user not provisioned")
			}

			password, passErr := randomToken(randomEntropyByteCount)
			if passErr != nil {
				return nil, errors.New("failed to generate user credential")
			}

			created, createErr := store.CreateWithID(oidcUserID, username, displayName, password, targetRole)
			if createErr != nil {
				return nil, fmt.Errorf("create oidc user: %w", createErr)
			}
			return created, nil
		}
	}

	if user.ID == oidcUserID && (user.Username != username || user.DisplayName != displayName) {
		if err := store.UpdateProfile(user.ID, username, displayName); err != nil {
			p.logger.Warn("failed to update oidc user profile",
				zap.String("user_id", user.ID),
				zap.Error(err),
			)
		} else {
			user.Username = username
			user.DisplayName = displayName
		}
	}

	if !(user.Role == "admin" && targetRole != "admin") && user.Role != targetRole {
		if err := store.UpdateRole(user.ID, targetRole); err != nil {
			return nil, fmt.Errorf("update user role: %w", err)
		}
		user.Role = targetRole
	}

	return user, nil
}

func (p *Provider) resolveRole(claims map[string]any) string {
	candidates := claimAsStrings(claims[p.config.RoleClaim])
	bestRole := ""
	for _, candidate := range candidates {
		mapped := normalizeRole(p.config.RoleMapping[candidate])
		if mapped == "" {
			continue
		}
		if roleRank(mapped) > roleRank(bestRole) {
			bestRole = mapped
		}
	}
	if bestRole != "" {
		return bestRole
	}
	return p.config.DefaultRole
}

func claimAsStrings(v any) []string {
	switch typed := v.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{strings.TrimSpace(typed)}
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func claimString(claims map[string]any, key string) string {
	raw, ok := claims[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func roleRank(role string) int {
	switch normalizeRole(role) {
	case "admin":
		return 3
	case "operator":
		return 2
	case "viewer":
		return 1
	default:
		return 0
	}
}

func clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/auth/oidc",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func encodeState(state callbackState) (string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeState(encoded string) (*callbackState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var out callbackState
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.State == "" || out.Nonce == "" || out.CodeVerifier == "" {
		return nil, errors.New("incomplete state payload")
	}
	return &out, nil
}

func randomToken(bytesLen int) (string, error) {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
