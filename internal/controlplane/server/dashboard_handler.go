package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/reliability"
	"go.uber.org/zap"
)

// DashboardResponse is the payload returned by GET /api/v1/dashboard.
type DashboardResponse struct {
	// Fleet status breakdown
	Fleet DashboardFleet `json:"fleet"`

	// Active incidents by severity
	Incidents DashboardIncidents `json:"incidents"`

	// Fleet-wide compliance score
	Compliance DashboardCompliance `json:"compliance"`

	// Active alerts in last 24h
	Alerts DashboardAlerts `json:"alerts"`

	// Recent drill results (last 5)
	Drills DashboardDrills `json:"drills"`

	// Top unhealthy probes
	UnhealthyProbes []DashboardProbeHealth `json:"unhealthy_probes"`

	// System statistics
	Stats DashboardStats `json:"stats"`

	// Timestamp of this snapshot
	GeneratedAt time.Time `json:"generated_at"`
}

// DashboardFleet holds fleet-wide probe status counts.
type DashboardFleet struct {
	Total    int `json:"total"`
	Online   int `json:"online"`
	Offline  int `json:"offline"`
	Degraded int `json:"degraded"`
	// PolicyApplied is the count of probes with a non-observe policy level.
	PolicyApplied int `json:"policy_applied"`
	// PolicyAppliedPct is the % of probes with a policy applied (0–100).
	PolicyAppliedPct float64 `json:"policy_applied_pct"`
}

// DashboardIncidents holds incident counts broken down by severity.
type DashboardIncidents struct {
	Total      int            `json:"total"`
	Open       int            `json:"open"`
	BySeverity map[string]int `json:"by_severity"`
}

// DashboardCompliance holds fleet-wide compliance posture.
type DashboardCompliance struct {
	Available bool    `json:"available"`
	ScorePct  float64 `json:"score_pct"`
	Passing   int     `json:"passing"`
	Failing   int     `json:"failing"`
	Unknown   int     `json:"unknown"`
}

// DashboardAlerts holds active alert summary.
type DashboardAlerts struct {
	Available int `json:"available"` // 1 = engine running, 0 = unavailable
	Active    int `json:"active"`
	// BySeverity counts active alerts grouped by rule severity.
	BySeverity map[string]int `json:"by_severity"`
}

// DashboardDrills holds recent drill execution history.
type DashboardDrills struct {
	Available bool                   `json:"available"`
	Recent    []DashboardDrillResult `json:"recent"`
	LastPass  *time.Time             `json:"last_pass,omitempty"`
	LastFail  *time.Time             `json:"last_fail,omitempty"`
}

// DashboardDrillResult is a summary of one drill execution.
type DashboardDrillResult struct {
	Scenario   string    `json:"scenario"`
	Status     string    `json:"status"`
	RunAt      time.Time `json:"run_at"`
	DurationMs int64     `json:"duration_ms"`
}

// DashboardProbeHealth is a brief health snapshot for one probe.
type DashboardProbeHealth struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Status       string `json:"status"`
	Score        int    `json:"score"`
	HealthStatus string `json:"health_status"`
}

// DashboardStats holds aggregate system statistics.
type DashboardStats struct {
	TotalAuditEvents int `json:"total_audit_events"`
	PendingApprovals int `json:"pending_approvals"`
}

// handleDashboardAPI serves GET /api/v1/dashboard.
func (s *Server) handleDashboardAPI(w http.ResponseWriter, r *http.Request) {
	resp := s.buildDashboardResponse(r)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// buildDashboardResponse assembles all the dashboard data.
func (s *Server) buildDashboardResponse(r *http.Request) DashboardResponse {
	now := time.Now().UTC()
	resp := DashboardResponse{
		GeneratedAt: now,
	}

	// ── Fleet status ─────────────────────────────────────────
	probes := s.probesForRequest(r)
	resp.Fleet = buildDashboardFleet(probes)

	// ── Incidents ────────────────────────────────────────────
	resp.Incidents = s.buildDashboardIncidents()

	// ── Compliance ───────────────────────────────────────────
	resp.Compliance = s.buildDashboardCompliance()

	// ── Alerts ───────────────────────────────────────────────
	resp.Alerts = s.buildDashboardAlerts()

	// ── Drills ───────────────────────────────────────────────
	resp.Drills = s.buildDashboardDrills()

	// ── Unhealthy probes (bottom 5 by health score) ──────────
	resp.UnhealthyProbes = buildUnhealthyProbes(probes, 5)

	// ── System stats ─────────────────────────────────────────
	resp.Stats = DashboardStats{
		TotalAuditEvents: s.countAudit(),
		PendingApprovals: s.approvalQueue.PendingCount(),
	}

	return resp
}

func buildDashboardFleet(probes []*fleet.ProbeState) DashboardFleet {
	df := DashboardFleet{
		Total: len(probes),
	}
	for _, ps := range probes {
		switch strings.ToLower(ps.Status) {
		case "online":
			df.Online++
		case "offline":
			df.Offline++
		case "degraded":
			df.Degraded++
		}
		// Any policy level other than "observe" counts as applied.
		if ps.PolicyLevel != "" && strings.ToLower(string(ps.PolicyLevel)) != "observe" {
			df.PolicyApplied++
		}
	}
	if df.Total > 0 {
		df.PolicyAppliedPct = float64(df.PolicyApplied) / float64(df.Total) * 100
	}
	return df
}

func (s *Server) buildDashboardIncidents() DashboardIncidents {
	di := DashboardIncidents{
		BySeverity: map[string]int{},
	}
	if s.incidentStore == nil {
		return di
	}

	incidents, err := s.incidentStore.List(reliability.IncidentFilter{})
	if err != nil {
		s.logger.Warn("dashboard: failed to list incidents", zap.Error(err))
		return di
	}

	for _, inc := range incidents {
		di.Total++
		if strings.ToLower(string(inc.Status)) == "open" {
			di.Open++
		}
		di.BySeverity[string(inc.Severity)]++
	}
	return di
}

func (s *Server) buildDashboardCompliance() DashboardCompliance {
	dc := DashboardCompliance{}
	if s.complianceHandlers == nil {
		return dc
	}
	summary, err := s.complianceStore.Summary()
	if err != nil {
		s.logger.Warn("dashboard: failed to get compliance summary", zap.Error(err))
		return dc
	}
	dc.Available = true
	dc.ScorePct = summary.ScorePct
	dc.Passing = summary.Passing
	dc.Failing = summary.Failing
	dc.Unknown = summary.Unknown
	return dc
}

func (s *Server) buildDashboardAlerts() DashboardAlerts {
	da := DashboardAlerts{
		BySeverity: map[string]int{},
	}
	if s.alertEngine == nil {
		return da
	}
	da.Available = 1

	// SnapshotFiring returns currently-firing AlertEvents.
	// AlertEvent.Status will be "firing" for all of these.
	// We count them and group by rule ID (the engine doesn't expose severity
	// per-event directly without a store lookup, so we report total + "firing").
	firing := s.alertEngine.SnapshotFiring()
	da.Active = len(firing)
	if da.Active > 0 {
		da.BySeverity["firing"] = da.Active
	}
	return da
}

func (s *Server) buildDashboardDrills() DashboardDrills {
	dd := DashboardDrills{
		Recent: []DashboardDrillResult{},
	}
	if s.drillStore == nil {
		return dd
	}
	dd.Available = true

	results, err := s.drillStore.List(5)
	if err != nil {
		s.logger.Warn("dashboard: failed to list drills", zap.Error(err))
		return dd
	}

	for _, r := range results {
		dr := DashboardDrillResult{
			Scenario:   string(r.Scenario),
			Status:     string(r.Status),
			RunAt:      r.RanAt,
			DurationMs: r.DurationMS,
		}
		dd.Recent = append(dd.Recent, dr)

		if strings.EqualFold(string(r.Status), "pass") {
			t := r.RanAt
			if dd.LastPass == nil || t.After(*dd.LastPass) {
				dd.LastPass = &t
			}
		} else if strings.EqualFold(string(r.Status), "fail") {
			t := r.RanAt
			if dd.LastFail == nil || t.After(*dd.LastFail) {
				dd.LastFail = &t
			}
		}
	}
	return dd
}

// buildUnhealthyProbes returns the top-n probes with the lowest health scores,
// skipping probes that have no health data.
func buildUnhealthyProbes(probes []*fleet.ProbeState, n int) []DashboardProbeHealth {
	type scored struct {
		probe *fleet.ProbeState
		score int
	}

	candidates := make([]scored, 0, len(probes))
	for _, ps := range probes {
		if ps.Health == nil {
			continue
		}
		candidates = append(candidates, scored{probe: ps, score: ps.Health.Score})
	}

	// Sort ascending by score (lowest first = most unhealthy).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score < candidates[j].score
	})

	if len(candidates) > n {
		candidates = candidates[:n]
	}

	out := make([]DashboardProbeHealth, 0, len(candidates))
	for _, c := range candidates {
		hostname := c.probe.Hostname
		if hostname == "" {
			hostname = c.probe.ID
		}
		out = append(out, DashboardProbeHealth{
			ID:           c.probe.ID,
			Hostname:     hostname,
			Status:       c.probe.Status,
			Score:        c.probe.Health.Score,
			HealthStatus: c.probe.Health.Status,
		})
	}
	return out
}

// handleDashboardPage serves the dashboard web page.
func (s *Server) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.pages == nil {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Dashboard</h1><p>Template not loaded</p>"))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := BasePage{
		CurrentUser: s.currentTemplateUser(r),
		Version:     Version,
		ActiveNav:   "dashboard",
	}
	if err := s.pages.Render(w, "dashboard", data); err != nil {
		s.logger.Error("failed to render dashboard page", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}
