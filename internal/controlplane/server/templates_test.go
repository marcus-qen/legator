package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
)

func TestTemplateFuncs_MapContainsExpectedHelpers(t *testing.T) {
	funcs := templateFuncs()
	for _, key := range []string{"statusClass", "humanizeStatus", "formatLastSeen", "humanBytes", "hasPermission"} {
		if _, ok := funcs[key]; !ok {
			t.Fatalf("missing template func %q", key)
		}
	}
}

func TestTemplateStatusHelpers(t *testing.T) {
	cases := []struct {
		in        string
		wantClass string
		wantHuman string
	}{
		{in: "online", wantClass: "online", wantHuman: "online"},
		{in: "OFFLINE", wantClass: "offline", wantHuman: "offline"},
		{in: "degraded", wantClass: "degraded", wantHuman: "degraded"},
		{in: "", wantClass: "pending", wantHuman: "pending"},
		{in: "mystery", wantClass: "pending", wantHuman: "mystery"},
	}

	for _, tc := range cases {
		if got := templateStatusClass(tc.in); got != tc.wantClass {
			t.Fatalf("templateStatusClass(%q): got %q, want %q", tc.in, got, tc.wantClass)
		}
		if got := templateHumanizeStatus(tc.in); got != tc.wantHuman {
			t.Fatalf("templateHumanizeStatus(%q): got %q, want %q", tc.in, got, tc.wantHuman)
		}
	}
}

func TestFormatLastSeen(t *testing.T) {
	if got := formatLastSeen(time.Time{}); got != "-" {
		t.Fatalf("expected - for zero time, got %q", got)
	}

	ts := time.Date(2026, time.February, 26, 10, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	if got := formatLastSeen(ts); got != "2026-02-26T08:00:00Z" {
		t.Fatalf("unexpected formatted time: %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1024, "1.0 KiB"},
		{1024 * 1024, "1.0 MiB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Fatalf("humanBytes(%d): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCalculateUptime(t *testing.T) {
	if got := calculateUptime(time.Time{}); got != "n/a" {
		t.Fatalf("expected n/a for zero start time, got %q", got)
	}

	start := time.Now().Add(-(26*time.Hour + 3*time.Minute + 4*time.Second))
	got := calculateUptime(start)
	if !strings.Contains(got, "1d") || !strings.Contains(got, "2h") || !strings.Contains(got, "3m") {
		t.Fatalf("expected uptime to contain 1d 2h 3m, got %q", got)
	}
}

func TestProbeDetailTemplateIncludesIncrementalSSEAnchors(t *testing.T) {
	path := filepath.Join("..", "..", "..", "web", "templates", "probe-detail.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read probe detail template: %v", err)
	}

	html := string(content)
	required := []string{
		`id="probe-status-badge"`,
		`id="probe-health-badge"`,
		`id="probe-last-seen"`,
		`id="probe-uptime"`,
		`id="probe-conn-badge"`,
		`id="probe-hostname-field"`,
		`id="probe-memory-field"`,
		`new EventSource('/api/v1/events')`,
		`['probe.connected', 'probe.disconnected', 'probe.offline']`,
		`['command.completed', 'command.failed']`,
		"fetch(`/api/v1/probes/${encodeURIComponent(PROBE_ID)}`",
	}

	for _, snippet := range required {
		if !strings.Contains(html, snippet) {
			t.Fatalf("template missing expected snippet: %s", snippet)
		}
	}
}

func TestApprovalsTemplateIncludesExplainabilityPanel(t *testing.T) {
	path := filepath.Join("..", "..", "..", "web", "templates", "approvals.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read approvals template: %v", err)
	}

	html := string(content)
	required := []string{
		"policy-explainability",
		"renderPolicyExplainability",
		"policy_rationale",
		"policy_decision",
		"Machine-readable rationale",
		"drove_outcome",
	}

	for _, snippet := range required {
		if !strings.Contains(html, snippet) {
			t.Fatalf("approvals template missing expected snippet: %s", snippet)
		}
	}
}

func TestJobsTemplateIncludesRunHistoryAndTriageControls(t *testing.T) {
	path := filepath.Join("..", "..", "..", "web", "templates", "jobs.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read jobs template: %v", err)
	}

	html := string(content)
	required := []string{
		`id="jobs-list-body"`,
		`id="jobs-run-filters"`,
		`id="jobs-runs-body"`,
		`id="jobs-triage-output"`,
		`/api/v1/jobs/runs`,
		`/api/v1/jobs/${encodeURIComponent(jobID)}/run`,
		`/api/v1/jobs/${encodeURIComponent(jobID)}/runs/${encodeURIComponent(runID)}/cancel`,
		`/api/v1/jobs/${encodeURIComponent(jobID)}/runs/${encodeURIComponent(runID)}/retry`,
	}

	for _, snippet := range required {
		if !strings.Contains(html, snippet) {
			t.Fatalf("jobs template missing expected snippet: %s", snippet)
		}
	}
}

func TestTemplateHasPermission(t *testing.T) {
	admin := &TemplateUser{Permissions: map[auth.Permission]struct{}{auth.PermAdmin: {}}}
	if !templateHasPermission(admin, string(auth.PermFleetWrite)) {
		t.Fatal("expected admin to have fleet write")
	}

	viewer := &TemplateUser{Permissions: map[auth.Permission]struct{}{auth.PermFleetRead: {}, auth.PermAuditRead: {}}}
	if !templateHasPermission(viewer, string(auth.PermAuditRead)) {
		t.Fatal("expected explicit permission to pass")
	}
	if templateHasPermission(viewer, string(auth.PermFleetWrite)) {
		t.Fatal("expected missing permission to fail")
	}
	if templateHasPermission(nil, string(auth.PermFleetRead)) {
		t.Fatal("nil user should never have permission")
	}
}
