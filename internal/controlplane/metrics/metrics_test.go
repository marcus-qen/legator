package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type mockFleet struct{}

func (m *mockFleet) Count() map[string]int     { return map[string]int{"online": 3, "offline": 1} }
func (m *mockFleet) TagCounts() map[string]int { return map[string]int{"prod": 2, "dev": 1} }

type mockHub struct{}

func (m *mockHub) Connected() int { return 3 }

type mockApprovals struct{}

func (m *mockApprovals) PendingCount() int { return 2 }

type mockAudit struct{}

func (m *mockAudit) Count() int { return 47 }

func TestMetricsHandler(t *testing.T) {
	c := NewCollector(&mockFleet{}, &mockHub{}, &mockApprovals{}, &mockAudit{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	w := httptest.NewRecorder()

	c.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()

	checks := []string{
		`legator_probes_total{status="online"} 3`,
		`legator_probes_total{status="offline"} 1`,
		`legator_probes_registered 4`,
		`legator_websocket_connections 3`,
		`legator_approvals_pending 2`,
		`legator_audit_events_total 47`,
		`legator_probes_by_tag{tag="prod"} 2`,
		`legator_probes_by_tag{tag="dev"} 1`,
		`legator_uptime_seconds`,
	}

	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("missing metric: %s\nbody:\n%s", check, body)
		}
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content-type, got %s", ct)
	}
}

func TestMetricsZeroState(t *testing.T) {
	zeroFleet := &struct{ mockFleet }{}
	// Override to return empty
	c := NewCollector(
		&emptyFleet{},
		&emptyHub{},
		&emptyApprovals{},
		&emptyAudit{},
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	w := httptest.NewRecorder()

	c.Handler().ServeHTTP(w, req)

	body := w.Body.String()
	_ = zeroFleet

	if !strings.Contains(body, `legator_probes_registered 0`) {
		t.Error("expected zero probes registered")
	}
	// All statuses should be present with zero values
	for _, s := range []string{"online", "offline", "degraded", "pending"} {
		want := `legator_probes_total{status="` + s + `"} 0`
		if !strings.Contains(body, want) {
			t.Errorf("missing zero metric for %s", s)
		}
	}
}

type emptyFleet struct{}

func (e *emptyFleet) Count() map[string]int     { return map[string]int{} }
func (e *emptyFleet) TagCounts() map[string]int { return map[string]int{} }

type emptyHub struct{}

func (e *emptyHub) Connected() int { return 0 }

type emptyApprovals struct{}

func (e *emptyApprovals) PendingCount() int { return 0 }

type emptyAudit struct{}

func (e *emptyAudit) Count() int { return 0 }

type mockAsyncJobs struct{}

func (m *mockAsyncJobs) AsyncJobStateCounts() map[string]int {
	return map[string]int{"queued": 4, "running": 2, "succeeded": 9, "failed": 1, "cancelled": 1}
}

func (m *mockAsyncJobs) AsyncJobQueueLatency() ([]float64, []uint64, float64, uint64) {
	return []float64{0.1, 0.5}, []uint64{3, 5, 5}, 0.42, 5
}

func (m *mockAsyncJobs) AsyncJobDispatchLatency() ([]float64, []uint64, float64, uint64) {
	return []float64{0.05, 0.25}, []uint64{2, 4, 4}, 0.18, 4
}

func TestMetricsAsyncJobSeries(t *testing.T) {
	c := NewCollector(&mockFleet{}, &mockHub{}, &mockApprovals{}, &mockAudit{}, &mockAsyncJobs{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, req)
	body := w.Body.String()

	checks := []string{
		`legator_async_jobs_total{state="queued"} 4`,
		`legator_async_jobs_total{state="running"} 2`,
		`legator_async_jobs_total{state="succeeded"} 9`,
		`legator_async_jobs_total{state="failed"} 1`,
		`legator_async_jobs_total{state="cancelled"} 1`,
		`legator_async_scheduler_queue_latency_seconds_bucket{le="0.1"} 3`,
		`legator_async_scheduler_queue_latency_seconds_bucket{le="+Inf"} 5`,
		`legator_async_scheduler_queue_latency_seconds_sum 0.42`,
		`legator_async_scheduler_queue_latency_seconds_count 5`,
		`legator_async_dispatch_latency_seconds_bucket{le="0.05"} 2`,
		`legator_async_dispatch_latency_seconds_bucket{le="+Inf"} 4`,
		`legator_async_dispatch_latency_seconds_sum 0.18`,
		`legator_async_dispatch_latency_seconds_count 4`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Fatalf("missing metric %q in body:\n%s", check, body)
		}
	}
}
