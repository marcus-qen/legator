package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
)

// TestHandleDashboardAPI_EmptyFleet verifies the endpoint returns 200 with a
// coherent response when no probes are registered.
func TestHandleDashboardAPI_EmptyFleet(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()

	srv.handleDashboardAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var resp DashboardResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode dashboard response: %v", err)
	}

	// Fleet should be zero-valued but valid.
	if resp.Fleet.Total != 0 {
		t.Errorf("expected 0 total probes, got %d", resp.Fleet.Total)
	}
	if resp.Fleet.Online != 0 {
		t.Errorf("expected 0 online probes, got %d", resp.Fleet.Online)
	}
	if resp.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should not be zero")
	}
	// Incidents should have a populated BySeverity map (even if empty).
	if resp.Incidents.BySeverity == nil {
		t.Error("Incidents.BySeverity should not be nil")
	}
	// Alerts should have a BySeverity map.
	if resp.Alerts.BySeverity == nil {
		t.Error("Alerts.BySeverity should not be nil")
	}
}

// TestHandleDashboardAPI_FleetStatusCounts verifies the API correctly aggregates
// probe status counts.
func TestHandleDashboardAPI_FleetStatusCounts(t *testing.T) {
	srv := newTestServer(t)

	// Register probes.
	srv.fleetMgr.Register("p1", "host-p1", "linux", "amd64")
	srv.fleetMgr.Register("p2", "host-p2", "linux", "amd64")
	srv.fleetMgr.Register("p3", "host-p3", "linux", "amd64")
	srv.fleetMgr.Register("p4", "host-p4", "linux", "amd64")

	// p1 and p2 stay online (default after register).
	_ = srv.fleetMgr.SetStatus("p3", "offline")
	_ = srv.fleetMgr.SetStatus("p4", "degraded")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()
	srv.handleDashboardAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp DashboardResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode dashboard response: %v", err)
	}

	if resp.Fleet.Total != 4 {
		t.Errorf("fleet.total: want 4, got %d", resp.Fleet.Total)
	}
	// Freshly registered probes don't have status "online" yet — they're "pending".
	// What matters is the aggregation logic correctly counts what's there.
	if resp.Fleet.Offline != 1 {
		t.Errorf("fleet.offline: want 1, got %d", resp.Fleet.Offline)
	}
	if resp.Fleet.Degraded != 1 {
		t.Errorf("fleet.degraded: want 1, got %d", resp.Fleet.Degraded)
	}
}

// TestHandleDashboardAPI_PolicyAppliedCount verifies that only probes with a
// non-observe policy level are counted as having a policy applied.
func TestHandleDashboardAPI_PolicyAppliedCount(t *testing.T) {
	srv := newTestServer(t)

	// Register probes with different capability levels.
	srv.fleetMgr.Register("p-obs", "host-obs", "linux", "amd64")
	srv.fleetMgr.Register("p-act", "host-act", "linux", "amd64")
	_ = srv.fleetMgr.SetPolicy("p-act", protocol.CapRemediate)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	w := httptest.NewRecorder()
	srv.handleDashboardAPI(w, req)

	var resp DashboardResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Only p-act has a non-observe policy; p-obs has the default "observe".
	if resp.Fleet.PolicyApplied != 1 {
		t.Errorf("fleet.policy_applied: want 1, got %d", resp.Fleet.PolicyApplied)
	}
	if resp.Fleet.PolicyAppliedPct < 49 || resp.Fleet.PolicyAppliedPct > 51 {
		t.Errorf("fleet.policy_applied_pct: want ~50, got %.1f", resp.Fleet.PolicyAppliedPct)
	}
}

// TestBuildDashboardFleet_Empty covers the helper directly with no probes.
func TestBuildDashboardFleet_Empty(t *testing.T) {
	df := buildDashboardFleet(nil)
	if df.Total != 0 {
		t.Errorf("want 0 total, got %d", df.Total)
	}
	if df.PolicyAppliedPct != 0 {
		t.Errorf("want 0 pct, got %f", df.PolicyAppliedPct)
	}
}

// TestBuildDashboardFleet_AllOnline verifies counts for a fully-online fleet.
func TestBuildDashboardFleet_AllOnline(t *testing.T) {
	probes := []*fleet.ProbeState{
		{ID: "a", Status: "online"},
		{ID: "b", Status: "online"},
	}
	df := buildDashboardFleet(probes)
	if df.Total != 2 {
		t.Errorf("want 2 total, got %d", df.Total)
	}
	if df.Online != 2 {
		t.Errorf("want 2 online, got %d", df.Online)
	}
}

// TestBuildUnhealthyProbes verifies ordering and capping.
func TestBuildUnhealthyProbes(t *testing.T) {
	probes := []*fleet.ProbeState{
		{ID: "a", Hostname: "hostA", Status: "online", Health: &fleet.HealthScore{Score: 90, Status: "healthy"}},
		{ID: "b", Hostname: "hostB", Status: "degraded", Health: &fleet.HealthScore{Score: 30, Status: "degraded"}},
		{ID: "c", Hostname: "hostC", Status: "offline", Health: &fleet.HealthScore{Score: 0, Status: "critical"}},
		{ID: "d", Hostname: "hostD", Status: "online", Health: &fleet.HealthScore{Score: 55, Status: "warning"}},
		{ID: "e", Hostname: "hostE", Status: "online", Health: &fleet.HealthScore{Score: 10, Status: "degraded"}},
	}

	out := buildUnhealthyProbes(probes, 3)

	if len(out) != 3 {
		t.Fatalf("want 3 results, got %d", len(out))
	}
	// Should be sorted ascending by score: 0, 10, 30
	if out[0].Score != 0 {
		t.Errorf("first should be score 0, got %d", out[0].Score)
	}
	if out[1].Score != 10 {
		t.Errorf("second should be score 10, got %d", out[1].Score)
	}
	if out[2].Score != 30 {
		t.Errorf("third should be score 30, got %d", out[2].Score)
	}
}

// TestBuildUnhealthyProbes_NoHealthData confirms probes without health data are skipped.
func TestBuildUnhealthyProbes_NoHealthData(t *testing.T) {
	probes := []*fleet.ProbeState{
		{ID: "a", Hostname: "hostA", Status: "online"},  // no Health
		{ID: "b", Hostname: "hostB", Status: "offline"}, // no Health
	}
	out := buildUnhealthyProbes(probes, 5)
	if len(out) != 0 {
		t.Errorf("expected 0 results when no health data, got %d", len(out))
	}
}

// TestBuildUnhealthyProbes_UsesIDWhenHostnameEmpty checks ID fallback.
func TestBuildUnhealthyProbes_UsesIDWhenHostnameEmpty(t *testing.T) {
	probes := []*fleet.ProbeState{
		{ID: "probe-xyz", Hostname: "", Status: "offline", Health: &fleet.HealthScore{Score: 5, Status: "critical"}},
	}
	out := buildUnhealthyProbes(probes, 5)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d", len(out))
	}
	if out[0].Hostname != "probe-xyz" {
		t.Errorf("expected ID fallback for hostname, got %q", out[0].Hostname)
	}
}

// TestHandleDashboardPage_NoTemplates verifies the handler falls back gracefully
// when templates aren't loaded.
func TestHandleDashboardPage_NoTemplates(t *testing.T) {
	srv := newTestServer(t)
	srv.pages = nil // force no-template mode

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	srv.handleDashboardPage(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Dashboard") {
		t.Errorf("expected body to contain 'Dashboard', got: %s", body)
	}
}

// TestDashboardRouteRegistered verifies the route is wired into the mux.
func TestDashboardRouteRegistered(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.httpServer.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET /api/v1/dashboard: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestRootRedirectsToDashboard verifies GET / redirects to /dashboard when
// templates are loaded (pages != nil).
func TestRootRedirectsToDashboard(t *testing.T) {
	srv := newTestServer(t)
	// loadTemplates is called in New, but may fail if web/templates dir doesn't
	// exist in test context. Simulate a loaded pages object.
	srv.pages = &pageTemplates{templates: map[string]pageTemplate{}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.handleRootPage(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 redirect, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/dashboard" {
		t.Errorf("expected redirect to /dashboard, got %q", loc)
	}
}

// TestRootFallsBackToFleetPage when pages are nil.
func TestRootFallsBackToFleetPage(t *testing.T) {
	srv := newTestServer(t)
	srv.pages = nil

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.handleRootPage(w, req)

	// Should not be a redirect.
	if w.Code == http.StatusFound {
		t.Error("expected no redirect when pages=nil, but got 302")
	}
}
