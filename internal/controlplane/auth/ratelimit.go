package auth

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter provides per-key request rate limiting using a sliding window.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*window
	limit   int           // max requests per window
	window  time.Duration // window size
}

type window struct {
	count    int
	resetAt  time.Time
}

// NewRateLimiter creates a rate limiter.
// Example: NewRateLimiter(100, time.Minute) → 100 requests per minute per key.
func NewRateLimiter(limit int, windowSize time.Duration) *RateLimiter {
	return &RateLimiter{
		windows: make(map[string]*window),
		limit:   limit,
		window:  windowSize,
	}
}

// Allow checks if a request from the given key is allowed.
func (rl *RateLimiter) Allow(keyID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	w, ok := rl.windows[keyID]
	if !ok || now.After(w.resetAt) {
		rl.windows[keyID] = &window{count: 1, resetAt: now.Add(rl.window)}
		return true
	}

	if w.count >= rl.limit {
		return false
	}

	w.count++
	return true
}

// Remaining returns how many requests are left in the current window.
func (rl *RateLimiter) Remaining(keyID string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	w, ok := rl.windows[keyID]
	if !ok || time.Now().After(w.resetAt) {
		return rl.limit
	}
	rem := rl.limit - w.count
	if rem < 0 {
		return 0
	}
	return rem
}

// RateLimitMiddleware wraps the auth middleware with rate limiting.
// Requires auth middleware to run first (key must be in context).
func RateLimitMiddleware(rl *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := FromContext(r.Context())
			if key == nil {
				// No auth key — skip rate limiting (either unauthenticated or skipped path)
				next.ServeHTTP(w, r)
				return
			}

			if !rl.Allow(key.ID) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
