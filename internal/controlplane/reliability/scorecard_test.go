package reliability

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildScorecardHealthy(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	scorecard := BuildScorecard(Inputs{
		Now:    now,
		Window: 15 * time.Minute,
		ControlPlane: ControlPlaneInputs{
			TotalRequests:       100,
			SuccessfulRequests:  100,
			ServerErrorRequests: 0,
			P95Latency:          320 * time.Millisecond,
		},
		ProbeFleet: ProbeFleetInputs{
			TotalProbes:     10,
			ConnectedProbes: 10,
		},
		Command: CommandInputs{
			TotalResults:      25,
			SuccessfulResults: 25,
		},
	})

	if scorecard.Overall.Status != "healthy" {
		t.Fatalf("expected healthy overall status, got %q", scorecard.Overall.Status)
	}
	if scorecard.Overall.Score != 100 {
		t.Fatalf("expected overall score 100, got %d", scorecard.Overall.Score)
	}
	if len(scorecard.Surfaces) != 2 {
		t.Fatalf("expected 2 surfaces, got %d", len(scorecard.Surfaces))
	}

	for _, surface := range scorecard.Surfaces {
		if surface.Status != "healthy" {
			t.Fatalf("expected healthy surface %q, got %q", surface.ID, surface.Status)
		}
		for _, indicator := range surface.Indicators {
			if indicator.Status != "pass" {
				t.Fatalf("expected pass for indicator %q, got %q", indicator.ID, indicator.Status)
			}
			if indicator.Objective.Window != "15m0s" {
				t.Fatalf("expected objective window metadata, got %q", indicator.Objective.Window)
			}
		}
	}
}

func TestBuildScorecardFailureAndUnknownMix(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 30, 0, 0, time.UTC)
	scorecard := BuildScorecard(Inputs{
		Now:    now,
		Window: 15 * time.Minute,
		ControlPlane: ControlPlaneInputs{
			TotalRequests:       100,
			SuccessfulRequests:  98,
			ServerErrorRequests: 3,
			P95Latency:          1800 * time.Millisecond,
		},
		ProbeFleet: ProbeFleetInputs{
			TotalProbes:     10,
			ConnectedProbes: 8,
		},
		Command: CommandInputs{},
	})

	if scorecard.Overall.Status != "critical" {
		t.Fatalf("expected critical overall status, got %q", scorecard.Overall.Status)
	}
	if scorecard.Overall.Compliance.Failing == 0 {
		t.Fatalf("expected at least one failing indicator, got %+v", scorecard.Overall.Compliance)
	}
	if scorecard.Overall.Compliance.Unknown == 0 {
		t.Fatalf("expected unknown indicator count for no command samples, got %+v", scorecard.Overall.Compliance)
	}

	var sawSevere bool
	for _, surface := range scorecard.Surfaces {
		for _, indicator := range surface.Indicators {
			if indicator.Status == "fail" && indicator.Score == 20 {
				sawSevere = true
			}
		}
	}
	if !sawSevere {
		t.Fatal("expected at least one severe failing indicator with score=20")
	}
}

func TestRequestTelemetrySnapshotAndFiltering(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Hour)
	telemetry := NewRequestTelemetry(50, 2*time.Hour, startedAt)

	handler := telemetry.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/fail":
			w.WriteHeader(http.StatusBadGateway)
		case "/api/v1/ok":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))

	paths := []string{"/api/v1/ok", "/api/v1/ok", "/api/v1/fail", "/static/style.css"}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	now := time.Now().UTC()
	stats := telemetry.Snapshot(15*time.Minute, now)
	if stats.TotalRequests != 3 {
		t.Fatalf("expected 3 sampled control-plane requests, got %d", stats.TotalRequests)
	}
	if stats.SuccessfulRequests != 2 {
		t.Fatalf("expected 2 successful requests, got %d", stats.SuccessfulRequests)
	}
	if stats.ServerErrors != 1 {
		t.Fatalf("expected 1 server error request, got %d", stats.ServerErrors)
	}
}

func TestRequestTelemetryP95Computation(t *testing.T) {
	now := time.Now().UTC()
	telemetry := NewRequestTelemetry(20, time.Hour, now.Add(-time.Minute))
	durations := []time.Duration{10, 20, 30, 40, 50}
	for _, d := range durations {
		telemetry.record(requestSample{
			Timestamp: now,
			Status:    http.StatusOK,
			Duration:  d * time.Millisecond,
		})
	}

	stats := telemetry.Snapshot(5*time.Minute, now.Add(time.Second))
	if stats.P95Latency != 50*time.Millisecond {
		t.Fatalf("expected p95 latency 50ms, got %s", stats.P95Latency)
	}
}
