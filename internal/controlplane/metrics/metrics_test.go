package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type mockFleet struct{}
func (m *mockFleet) Count() map[string]int { return map[string]int{"online": 3, "offline": 1} }
func (m *mockFleet) TagCounts() map[string]int { return map[string]int{"prod": 2, "dev": 1} }

type mockHub struct{}
func (m *mockHub) Connected() int { return 3 }

type mockApprovals struct{}
func (m *mockApprovals) PendingCount() int { return 2 }

type mockAudit struct{}
func (m *mockAudit) Count() int { return 47 }

func TestMetricsHandler(t *testing.T) {
	c := NewCollector(&mockFleet{}, &mockHub{}, &mockApprovals{}, &mockAudit{})

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
func (e *emptyFleet) Count() map[string]int    { return map[string]int{} }
func (e *emptyFleet) TagCounts() map[string]int { return map[string]int{} }

type emptyHub struct{}
func (e *emptyHub) Connected() int { return 0 }

type emptyApprovals struct{}
func (e *emptyApprovals) PendingCount() int { return 0 }

type emptyAudit struct{}
func (e *emptyAudit) Count() int { return 0 }
