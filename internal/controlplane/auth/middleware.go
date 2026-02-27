package auth

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const (
	apiKeyContextKey contextKey = "apiKey"
	userContextKey   contextKey = "user"
)

// FromContext retrieves the authenticated APIKey from the request context.
func FromContext(ctx context.Context) *APIKey {
	key, _ := ctx.Value(apiKeyContextKey).(*APIKey)
	return key
}

// UserFromContext retrieves the authenticated session user from request context.
func UserFromContext(ctx context.Context) *AuthenticatedUser {
	user, _ := ctx.Value(userContextKey).(*AuthenticatedUser)
	return user
}

// IsAuthenticated reports whether either API key or session auth is present.
func IsAuthenticated(ctx context.Context) bool {
	return FromContext(ctx) != nil || UserFromContext(ctx) != nil
}

// HasPermissionFromContext checks required permission for either auth path.
func HasPermissionFromContext(ctx context.Context, perm Permission) bool {
	if key := FromContext(ctx); key != nil {
		return HasPermission(key, perm)
	}

	user := UserFromContext(ctx)
	if user == nil {
		return false
	}
	for _, p := range user.Permissions {
		if p == PermAdmin || p == perm {
			return true
		}
	}
	return false
}

// AuthMiddleware supports dual-path auth:
//  1. API key via Authorization: Bearer lgk_...
//  2. Session cookie via legator_session
//
// The API key path remains unchanged.
type AuthMiddleware struct {
	store      *KeyStore
	skipExact  map[string]bool
	skipPrefix []string

	sessionValidator   SessionValidator
	permissionResolver UserPermissionResolver
}

// NewMiddleware builds auth middleware with optional skip paths.
func NewMiddleware(store *KeyStore, skipPaths []string) *AuthMiddleware {
	skipExact := make(map[string]bool, len(skipPaths))
	skipPrefix := make([]string, 0)
	for _, p := range skipPaths {
		if strings.HasSuffix(p, "*") {
			skipPrefix = append(skipPrefix, strings.TrimSuffix(p, "*"))
			continue
		}
		skipExact[p] = true
	}

	return &AuthMiddleware{
		store:      store,
		skipExact:  skipExact,
		skipPrefix: skipPrefix,
	}
}

// SetSessionAuth wires session validation + role permission resolver.
func (m *AuthMiddleware) SetSessionAuth(validator SessionValidator, resolver UserPermissionResolver) {
	m.sessionValidator = validator
	m.permissionResolver = resolver
}

// Wrap returns the wrapped HTTP handler.
func (m *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.shouldSkip(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if m.tryAPIKeyAuth(w, r, next) {
			return
		}

		if m.trySessionAuth(r, next, w) {
			return
		}

		if isWebLoginRedirectPath(r.URL.Path) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
	})
}

// Middleware returns an HTTP middleware that checks API key auth.
// Extracts key from "Authorization: Bearer lgk_..." header.
// Skips auth for paths in skipPaths.
func Middleware(store *KeyStore, skipPaths []string) func(http.Handler) http.Handler {
	mw := NewMiddleware(store, skipPaths)
	return mw.Wrap
}

func (m *AuthMiddleware) shouldSkip(path string) bool {
	if path == "/login" || path == "/logout" || strings.HasPrefix(path, "/static/") {
		return true
	}
	if m.skipExact[path] {
		return true
	}
	for _, p := range m.skipPrefix {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func isWebLoginRedirectPath(path string) bool {
	switch path {
	case "/", "/approvals", "/audit", "/alerts", "/model-dock", "/cloud-connectors", "/network-devices", "/discovery", "/fleet/chat":
		return true
	}
	if strings.HasPrefix(path, "/probe/") {
		return true
	}
	if strings.HasPrefix(path, "/chat/") {
		return true
	}
	return false
}

func (m *AuthMiddleware) tryAPIKeyAuth(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
		return true
	}

	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if token == "" {
		http.Error(w, `{"error":"empty bearer token"}`, http.StatusUnauthorized)
		return true
	}

	if !strings.HasPrefix(token, "lgk_") {
		// Non-API-key bearer tokens are ignored here; allow cookie auth fallback.
		return false
	}

	if m.store == nil {
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
		return true
	}

	key, err := m.store.Validate(token)
	if err != nil {
		if strings.Contains(err.Error(), "expired") {
			http.Error(w, `{"error":"api key expired"}`, http.StatusForbidden)
			return true
		}
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
		return true
	}

	ctx := context.WithValue(r.Context(), apiKeyContextKey, key)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}

func (m *AuthMiddleware) trySessionAuth(r *http.Request, next http.Handler, w http.ResponseWriter) bool {
	if m.sessionValidator == nil {
		return false
	}

	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}

	session, err := m.sessionValidator.Validate(cookie.Value)
	if err != nil || session == nil {
		return false
	}

	user := &AuthenticatedUser{
		ID:       session.UserID,
		Username: session.Username,
		Role:     session.Role,
	}
	if m.permissionResolver != nil {
		user.Permissions = m.permissionResolver.PermissionsForRole(session.Role)
	}

	ctx := context.WithValue(r.Context(), userContextKey, user)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}
