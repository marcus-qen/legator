package api

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/marcus-qen/legator/internal/api/auth"
	"github.com/marcus-qen/legator/internal/api/rbac"
	"github.com/marcus-qen/legator/internal/metrics"
)

// UserRateLimitConfig configures per-user API request throttling.
type UserRateLimitConfig struct {
	Enabled bool

	ViewerRequestsPerMinute   int
	OperatorRequestsPerMinute int
	AdminRequestsPerMinute    int

	ViewerBurst   int
	OperatorBurst int
	AdminBurst    int

	// SurfaceHeader allows clients to identify origin surface (api|cli|web|chatops|mcp).
	// Empty defaults to X-Legator-Surface.
	SurfaceHeader string

	// BypassPaths skip throttling (e.g. /healthz).
	BypassPaths []string

	// EntryTTL controls idle limiter eviction.
	EntryTTL time.Duration
}

func defaultUserRateLimitConfig() UserRateLimitConfig {
	return UserRateLimitConfig{
		Enabled:                   true,
		ViewerRequestsPerMinute:   120,
		OperatorRequestsPerMinute: 60,
		AdminRequestsPerMinute:    90,
		ViewerBurst:               40,
		OperatorBurst:             20,
		AdminBurst:                30,
		SurfaceHeader:             "X-Legator-Surface",
		BypassPaths:               []string{"/healthz"},
		EntryTTL:                  30 * time.Minute,
	}
}

type userLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type userRateLimiter struct {
	cfg UserRateLimitConfig

	mu      sync.Mutex
	entries map[string]*userLimiterEntry
}

func newUserRateLimiter(cfg UserRateLimitConfig) *userRateLimiter {
	cfg = normalizeUserRateLimitConfig(cfg)
	return &userRateLimiter{
		cfg:     cfg,
		entries: map[string]*userLimiterEntry{},
	}
}

func normalizeUserRateLimitConfig(cfg UserRateLimitConfig) UserRateLimitConfig {
	d := defaultUserRateLimitConfig()
	if !cfg.Enabled && cfg.ViewerRequestsPerMinute == 0 && cfg.OperatorRequestsPerMinute == 0 && cfg.AdminRequestsPerMinute == 0 && cfg.ViewerBurst == 0 && cfg.OperatorBurst == 0 && cfg.AdminBurst == 0 && cfg.SurfaceHeader == "" && len(cfg.BypassPaths) == 0 && cfg.EntryTTL == 0 {
		cfg.Enabled = d.Enabled
	}
	if cfg.ViewerRequestsPerMinute <= 0 {
		cfg.ViewerRequestsPerMinute = d.ViewerRequestsPerMinute
	}
	if cfg.OperatorRequestsPerMinute <= 0 {
		cfg.OperatorRequestsPerMinute = d.OperatorRequestsPerMinute
	}
	if cfg.AdminRequestsPerMinute <= 0 {
		cfg.AdminRequestsPerMinute = d.AdminRequestsPerMinute
	}
	if cfg.ViewerBurst <= 0 {
		cfg.ViewerBurst = d.ViewerBurst
	}
	if cfg.OperatorBurst <= 0 {
		cfg.OperatorBurst = d.OperatorBurst
	}
	if cfg.AdminBurst <= 0 {
		cfg.AdminBurst = d.AdminBurst
	}
	if cfg.SurfaceHeader == "" {
		cfg.SurfaceHeader = d.SurfaceHeader
	}
	if cfg.EntryTTL <= 0 {
		cfg.EntryTTL = d.EntryTTL
	}
	if len(cfg.BypassPaths) == 0 {
		cfg.BypassPaths = d.BypassPaths
	}
	return cfg
}

func (l *userRateLimiter) middleware(next http.Handler, roleFn func(*rbac.UserIdentity) rbac.Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.cfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		for _, bp := range l.cfg.BypassPaths {
			if strings.HasPrefix(r.URL.Path, bp) {
				next.ServeHTTP(w, r)
				return
			}
		}

		user := auth.UserFromContext(r.Context())
		if user == nil {
			// auth middleware should reject these before this layer,
			// but be defensive and don't throttle anonymous requests here.
			next.ServeHTTP(w, r)
			return
		}

		role := roleFn(user)
		rpm, burst := l.limitForRole(role)
		surface := normalizeSurface(r.Header.Get(l.cfg.SurfaceHeader))
		subject := subjectOrEmail(user)
		key := fmt.Sprintf("%s|%s|%s", subject, role, surface)

		allowed := l.allow(key, rpm, burst)
		if allowed {
			next.ServeHTTP(w, r)
			return
		}

		retryAfter := retryAfterSeconds(rpm)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		metrics.RecordAPIRateLimitBlock(string(role), surface)
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":             "rate_limited",
			"reason":            fmt.Sprintf("per-user rate limit reached for role %s on surface %s", role, surface),
			"retryAfterSeconds": retryAfter,
			"surface":           surface,
			"role":              role,
			"subject":           subject,
			"limits": map[string]int{
				"requestsPerMinute": rpm,
				"burst":             burst,
			},
		})
	})
}

func (l *userRateLimiter) allow(key string, rpm, burst int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.prune(now)

	entry, ok := l.entries[key]
	if !ok {
		entry = &userLimiterEntry{
			limiter:  rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), burst),
			lastSeen: now,
		}
		l.entries[key] = entry
	}
	entry.lastSeen = now
	return entry.limiter.Allow()
}

func (l *userRateLimiter) prune(now time.Time) {
	for k, v := range l.entries {
		if now.Sub(v.lastSeen) > l.cfg.EntryTTL {
			delete(l.entries, k)
		}
	}
}

func (l *userRateLimiter) limitForRole(role rbac.Role) (rpm, burst int) {
	switch role {
	case rbac.RoleAdmin:
		return l.cfg.AdminRequestsPerMinute, l.cfg.AdminBurst
	case rbac.RoleOperator:
		return l.cfg.OperatorRequestsPerMinute, l.cfg.OperatorBurst
	default:
		return l.cfg.ViewerRequestsPerMinute, l.cfg.ViewerBurst
	}
}

func normalizeSurface(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "api", "cli", "web", "chatops", "mcp":
		return s
	default:
		return "api"
	}
}

func retryAfterSeconds(rpm int) int {
	if rpm <= 0 {
		return 1
	}
	seconds := int(math.Ceil(60.0 / float64(rpm)))
	if seconds < 1 {
		return 1
	}
	return seconds
}

func subjectOrEmail(u *rbac.UserIdentity) string {
	if u == nil {
		return "unknown"
	}
	if strings.TrimSpace(u.Subject) != "" {
		return u.Subject
	}
	if strings.TrimSpace(u.Email) != "" {
		return u.Email
	}
	return "unknown"
}
