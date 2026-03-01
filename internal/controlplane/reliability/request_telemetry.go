package reliability

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ControlPlaneRequestStats summarizes sampled request telemetry for one window.
type ControlPlaneRequestStats struct {
	From               time.Time     `json:"from"`
	To                 time.Time     `json:"to"`
	TotalRequests      int           `json:"total_requests"`
	SuccessfulRequests int           `json:"successful_requests"`
	ServerErrors       int           `json:"server_errors"`
	P95Latency         time.Duration `json:"p95_latency"`
}

type requestSample struct {
	Timestamp time.Time
	Status    int
	Duration  time.Duration
}

// RequestTelemetry records sampled HTTP request status/latency for scorecards.
type RequestTelemetry struct {
	mu         sync.RWMutex
	samples    []requestSample
	maxSamples int
	maxAge     time.Duration
	startedAt  time.Time
}

// NewRequestTelemetry creates an in-memory request telemetry recorder.
func NewRequestTelemetry(maxSamples int, maxAge time.Duration, startedAt time.Time) *RequestTelemetry {
	if maxSamples <= 0 {
		maxSamples = 10000
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &RequestTelemetry{
		samples:    make([]requestSample, 0, maxSamples),
		maxSamples: maxSamples,
		maxAge:     maxAge,
		startedAt:  startedAt.UTC(),
	}
}

// Middleware instruments control-plane API requests for reliability scoring.
func (t *RequestTelemetry) Middleware(next http.Handler) http.Handler {
	if t == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shouldSampleRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now().UTC()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		t.record(requestSample{
			Timestamp: time.Now().UTC(),
			Status:    recorder.status,
			Duration:  time.Since(start),
		})
	})
}

func shouldSampleRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	path := strings.TrimSpace(r.URL.Path)
	if path == "" {
		return false
	}
	if path == "/healthz" || path == "/version" || path == "/mcp" {
		return true
	}
	return strings.HasPrefix(path, "/api/v1/")
}

func (t *RequestTelemetry) record(sample requestSample) {
	if t == nil {
		return
	}

	now := sample.Timestamp.UTC()
	cutoff := time.Time{}
	if t.maxAge > 0 {
		cutoff = now.Add(-t.maxAge)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.samples = append(t.samples, sample)

	if !cutoff.IsZero() {
		firstValid := 0
		for firstValid < len(t.samples) && t.samples[firstValid].Timestamp.Before(cutoff) {
			firstValid++
		}
		if firstValid > 0 {
			t.samples = append([]requestSample(nil), t.samples[firstValid:]...)
		}
	}

	if len(t.samples) > t.maxSamples {
		over := len(t.samples) - t.maxSamples
		t.samples = append([]requestSample(nil), t.samples[over:]...)
	}
}

// Snapshot computes aggregate request stats for the provided window.
func (t *RequestTelemetry) Snapshot(window time.Duration, now time.Time) ControlPlaneRequestStats {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if window <= 0 {
		window = defaultWindow
	}

	from := now.Add(-window)
	if !t.startedAt.IsZero() && from.Before(t.startedAt) {
		from = t.startedAt
	}

	stats := ControlPlaneRequestStats{From: from, To: now}
	if t == nil {
		return stats
	}

	t.mu.RLock()
	samples := make([]requestSample, 0, len(t.samples))
	samples = append(samples, t.samples...)
	t.mu.RUnlock()

	latencies := make([]time.Duration, 0, len(samples))
	for _, sample := range samples {
		if sample.Timestamp.Before(from) || sample.Timestamp.After(now) {
			continue
		}
		stats.TotalRequests++
		if sample.Status < 500 {
			stats.SuccessfulRequests++
		}
		if sample.Status >= 500 {
			stats.ServerErrors++
		}
		latencies = append(latencies, sample.Duration)
	}

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		idx := int(math.Ceil(0.95*float64(len(latencies)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(latencies) {
			idx = len(latencies) - 1
		}
		stats.P95Latency = latencies[idx]
	}

	return stats
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
