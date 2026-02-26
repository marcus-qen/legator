package auth

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const apiKeyContextKey contextKey = "apiKey"

// FromContext retrieves the authenticated APIKey from the request context.
func FromContext(ctx context.Context) *APIKey {
	key, _ := ctx.Value(apiKeyContextKey).(*APIKey)
	return key
}

// Middleware returns an HTTP middleware that checks API key auth.
// Extracts key from "Authorization: Bearer lgk_..." header.
// Skips auth for paths in skipPaths.
func Middleware(store *KeyStore, skipPaths []string) func(http.Handler) http.Handler {
	skipSet := make(map[string]bool, len(skipPaths))
	for _, p := range skipPaths {
		skipSet[p] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip configured paths
			if skipSet[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			// Also skip paths with prefix match (for /ws/* etc.)
			for p := range skipSet {
				if strings.HasSuffix(p, "*") && strings.HasPrefix(r.URL.Path, strings.TrimSuffix(p, "*")) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Extract Bearer token
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
				return
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == "" {
				http.Error(w, `{"error":"empty bearer token"}`, http.StatusUnauthorized)
				return
			}

			key, err := store.Validate(token)
			if err != nil {
				if strings.Contains(err.Error(), "expired") {
					http.Error(w, `{"error":"api key expired"}`, http.StatusForbidden)
					return
				}
				http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
				return
			}

			// Store key in context
			ctx := context.WithValue(r.Context(), apiKeyContextKey, key)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
