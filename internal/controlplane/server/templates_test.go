package server

import (
	"strings"
	"testing"
	"time"
)

func TestTemplateFuncs_MapContainsExpectedHelpers(t *testing.T) {
	funcs := templateFuncs()
	for _, key := range []string{"statusClass", "humanizeStatus", "formatLastSeen", "humanBytes"} {
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
