package reliability_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/reliability"
)

// ── helpers ──────────────────────────────────────────────────

func tempIncidentStore(t *testing.T) *reliability.IncidentStore {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "incidents-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()
	store, err := reliability.NewIncidentStore(f.Name())
	if err != nil {
		t.Fatalf("NewIncidentStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustCreate(t *testing.T, store *reliability.IncidentStore, inc reliability.Incident) reliability.Incident {
	t.Helper()
	created, err := store.Create(inc)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created
}

// ── Store CRUD ────────────────────────────────────────────────

func TestIncidentStore_Create(t *testing.T) {
	store := tempIncidentStore(t)

	inc := reliability.Incident{
		Title:          "API latency spike",
		Severity:       reliability.SeverityP2,
		AffectedProbes: []string{"probe-a", "probe-b"},
	}
	created, err := store.Create(inc)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Error("expected non-empty ID")
	}
	if created.Status != reliability.StatusOpen {
		t.Errorf("expected status open, got %s", created.Status)
	}
	if created.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if created.Title != "API latency spike" {
		t.Errorf("unexpected title: %s", created.Title)
	}
}

func TestIncidentStore_Get(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{
		Title:    "DB connection pool exhausted",
		Severity: reliability.SeverityP1,
	})

	got, found, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected incident to be found")
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch: %s vs %s", got.ID, created.ID)
	}
	if got.Severity != reliability.SeverityP1 {
		t.Errorf("unexpected severity: %s", got.Severity)
	}
}

func TestIncidentStore_Get_NotFound(t *testing.T) {
	store := tempIncidentStore(t)
	_, found, err := store.Get("does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected not found")
	}
}

func TestIncidentStore_List_All(t *testing.T) {
	store := tempIncidentStore(t)
	mustCreate(t, store, reliability.Incident{Title: "Inc 1", Severity: reliability.SeverityP1})
	mustCreate(t, store, reliability.Incident{Title: "Inc 2", Severity: reliability.SeverityP3})
	mustCreate(t, store, reliability.Incident{Title: "Inc 3", Severity: reliability.SeverityP4})

	list, err := store.List(reliability.IncidentFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 incidents, got %d", len(list))
	}
}

func TestIncidentStore_List_FilterByStatus(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{Title: "Open inc", Severity: reliability.SeverityP2})

	// Update one to resolved
	_, _ = store.Update(created.ID, reliability.IncidentUpdate{
		Status: func() *reliability.IncidentStatus { s := reliability.StatusResolved; return &s }(),
	})
	mustCreate(t, store, reliability.Incident{Title: "Another open", Severity: reliability.SeverityP3})

	open, err := store.List(reliability.IncidentFilter{Status: reliability.StatusOpen})
	if err != nil {
		t.Fatalf("List open: %v", err)
	}
	if len(open) != 1 {
		t.Errorf("expected 1 open incident, got %d", len(open))
	}
	if open[0].Title != "Another open" {
		t.Errorf("unexpected title: %s", open[0].Title)
	}

	resolved, err := store.List(reliability.IncidentFilter{Status: reliability.StatusResolved})
	if err != nil {
		t.Fatalf("List resolved: %v", err)
	}
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved incident, got %d", len(resolved))
	}
}

func TestIncidentStore_List_FilterBySeverity(t *testing.T) {
	store := tempIncidentStore(t)
	mustCreate(t, store, reliability.Incident{Title: "P1 incident", Severity: reliability.SeverityP1})
	mustCreate(t, store, reliability.Incident{Title: "P2 incident", Severity: reliability.SeverityP2})
	mustCreate(t, store, reliability.Incident{Title: "P2 incident 2", Severity: reliability.SeverityP2})

	list, err := store.List(reliability.IncidentFilter{Severity: reliability.SeverityP2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 P2 incidents, got %d", len(list))
	}
}

func TestIncidentStore_List_FilterByProbe(t *testing.T) {
	store := tempIncidentStore(t)
	mustCreate(t, store, reliability.Incident{
		Title:          "Affects probe-x",
		Severity:       reliability.SeverityP3,
		AffectedProbes: []string{"probe-x", "probe-y"},
	})
	mustCreate(t, store, reliability.Incident{
		Title:          "No probe-x",
		Severity:       reliability.SeverityP4,
		AffectedProbes: []string{"probe-z"},
	})

	list, err := store.List(reliability.IncidentFilter{Probe: "probe-x"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 incident, got %d", len(list))
	}
	if list[0].Title != "Affects probe-x" {
		t.Errorf("unexpected title: %s", list[0].Title)
	}
}

func TestIncidentStore_List_FilterByDateRange(t *testing.T) {
	store := tempIncidentStore(t)
	past := time.Now().UTC().Add(-2 * time.Hour)
	recent := time.Now().UTC().Add(-10 * time.Minute)

	mustCreate(t, store, reliability.Incident{
		Title:     "Old incident",
		Severity:  reliability.SeverityP4,
		StartTime: past,
	})
	mustCreate(t, store, reliability.Incident{
		Title:     "Recent incident",
		Severity:  reliability.SeverityP3,
		StartTime: recent,
	})

	cutoff := time.Now().UTC().Add(-1 * time.Hour)
	list, err := store.List(reliability.IncidentFilter{From: cutoff})
	if err != nil {
		t.Fatalf("List with From filter: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 recent incident, got %d", len(list))
	}
	if list[0].Title != "Recent incident" {
		t.Errorf("unexpected title: %s", list[0].Title)
	}
}

func TestIncidentStore_Update(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{
		Title:    "Intermittent DB errors",
		Severity: reliability.SeverityP2,
	})

	rc := "Disk I/O saturation on primary"
	res := "Replaced disk, rebuilt RAID"
	newStatus := reliability.StatusResolved
	endTime := time.Now().UTC()

	updated, err := store.Update(created.ID, reliability.IncidentUpdate{
		Status:     &newStatus,
		RootCause:  &rc,
		Resolution: &res,
		EndTime:    &endTime,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != reliability.StatusResolved {
		t.Errorf("expected resolved, got %s", updated.Status)
	}
	if updated.RootCause != rc {
		t.Errorf("unexpected root cause: %s", updated.RootCause)
	}
	if updated.Resolution != res {
		t.Errorf("unexpected resolution: %s", updated.Resolution)
	}
	if updated.EndTime == nil {
		t.Error("expected non-nil EndTime")
	}
}

func TestIncidentStore_Update_NotFound(t *testing.T) {
	store := tempIncidentStore(t)
	_, err := store.Update("ghost-id", reliability.IncidentUpdate{})
	if err == nil {
		t.Error("expected error for non-existent incident")
	}
}

func TestIncidentStore_SoftDelete(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{
		Title:    "Temp incident",
		Severity: reliability.SeverityP4,
	})

	if err := store.SoftDelete(created.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Should not appear in Get
	_, found, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if found {
		t.Error("expected incident to be gone after soft delete")
	}

	// Should not appear in List
	list, err := store.List(reliability.IncidentFilter{})
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestIncidentStore_SoftDelete_NotFound(t *testing.T) {
	store := tempIncidentStore(t)
	err := store.SoftDelete("ghost-id")
	if err == nil {
		t.Error("expected error for non-existent incident")
	}
}

// ── Timeline ──────────────────────────────────────────────────

func TestIncidentStore_AddTimelineEntry(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{
		Title:    "Network partition",
		Severity: reliability.SeverityP1,
	})

	entry := reliability.TimelineEntry{
		IncidentID:  created.ID,
		Type:        reliability.TimelineAlertFired,
		Description: "Alert fired: network_partition_detected",
	}
	added, err := store.AddTimelineEntry(entry)
	if err != nil {
		t.Fatalf("AddTimelineEntry: %v", err)
	}
	if added.ID == "" {
		t.Error("expected non-empty ID")
	}
	if added.Timestamp.IsZero() {
		t.Error("expected non-zero Timestamp")
	}
}

func TestIncidentStore_GetTimeline_Ordering(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{
		Title:    "Ordering test",
		Severity: reliability.SeverityP3,
	})

	base := time.Now().UTC()
	entries := []reliability.TimelineEntry{
		{IncidentID: created.ID, Type: reliability.TimelineAlertFired, Description: "First", Timestamp: base},
		{IncidentID: created.ID, Type: reliability.TimelineCommandSent, Description: "Second", Timestamp: base.Add(5 * time.Minute)},
		{IncidentID: created.ID, Type: reliability.TimelineManualNote, Description: "Third", Timestamp: base.Add(10 * time.Minute)},
	}

	// Insert out of order to verify ordering
	for i := len(entries) - 1; i >= 0; i-- {
		if _, err := store.AddTimelineEntry(entries[i]); err != nil {
			t.Fatalf("AddTimelineEntry[%d]: %v", i, err)
		}
	}

	timeline, err := store.GetTimeline(created.ID)
	if err != nil {
		t.Fatalf("GetTimeline: %v", err)
	}
	if len(timeline) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(timeline))
	}
	for i, want := range []string{"First", "Second", "Third"} {
		if timeline[i].Description != want {
			t.Errorf("entry[%d]: expected %q, got %q", i, want, timeline[i].Description)
		}
	}
}

func TestIncidentStore_GetTimeline_Empty(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{
		Title:    "No events yet",
		Severity: reliability.SeverityP4,
	})

	timeline, err := store.GetTimeline(created.ID)
	if err != nil {
		t.Fatalf("GetTimeline: %v", err)
	}
	if len(timeline) != 0 {
		t.Errorf("expected empty timeline, got %d entries", len(timeline))
	}
}

// ── Postmortem bundle ─────────────────────────────────────────

func TestGeneratePostmortemBundle(t *testing.T) {
	store := tempIncidentStore(t)

	// Create an incident with a known end time so README shows duration
	endTime := time.Now().UTC().Add(30 * time.Minute)
	created := mustCreate(t, store, reliability.Incident{
		Title:          "Full lifecycle test",
		Severity:       reliability.SeverityP2,
		AffectedProbes: []string{"probe-1", "probe-2"},
		RootCause:      "Memory leak in worker pool",
		Resolution:     "Deployed hotfix v1.2.3 and restarted workers",
		EndTime:        &endTime,
	})

	// Add timeline entries
	base := created.StartTime
	tlEntries := []reliability.TimelineEntry{
		{IncidentID: created.ID, Type: reliability.TimelineAlertFired, Description: "High memory alert", Timestamp: base},
		{IncidentID: created.ID, Type: reliability.TimelineCommandSent, Description: "Triggered diagnostic script", Timestamp: base.Add(5 * time.Minute)},
		{IncidentID: created.ID, Type: reliability.TimelineStateChange, Description: "Status changed to investigating", Timestamp: base.Add(10 * time.Minute)},
	}
	for _, e := range tlEntries {
		if _, err := store.AddTimelineEntry(e); err != nil {
			t.Fatalf("AddTimelineEntry: %v", err)
		}
	}

	inc, _, _ := store.Get(created.ID)
	timeline, _ := store.GetTimeline(created.ID)

	// Generate the bundle
	var buf bytes.Buffer
	err := reliability.GeneratePostmortemBundle(&buf, inc, timeline, nil)
	if err != nil {
		t.Fatalf("GeneratePostmortemBundle: %v", err)
	}

	// Open the ZIP
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	// Collect file names
	files := make(map[string]*zip.File)
	for _, f := range zr.File {
		files[f.Name] = f
	}

	requiredFiles := []string{"incident.json", "timeline.jsonl", "audit-events.jsonl", "README.md"}
	for _, name := range requiredFiles {
		if _, ok := files[name]; !ok {
			t.Errorf("missing file in ZIP: %s", name)
		}
	}

	// Verify incident.json content
	if f, ok := files["incident.json"]; ok {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		_ = rc.Close()

		var rec reliability.PostmortemRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Errorf("incident.json is not valid JSON: %v", err)
		}
		if rec.Incident.ID != inc.ID {
			t.Errorf("incident.json: ID mismatch")
		}
		if rec.Incident.Title != "Full lifecycle test" {
			t.Errorf("incident.json: unexpected title %q", rec.Incident.Title)
		}
		if len(rec.Timeline) != 3 {
			t.Errorf("incident.json: expected 3 timeline entries, got %d", len(rec.Timeline))
		}
	}

	// Verify timeline.jsonl content
	if f, ok := files["timeline.jsonl"]; ok {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		_ = rc.Close()

		lines := splitNonEmpty(string(data))
		if len(lines) != 3 {
			t.Errorf("timeline.jsonl: expected 3 lines, got %d", len(lines))
		}
		for i, line := range lines {
			var entry reliability.TimelineEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Errorf("timeline.jsonl line %d: invalid JSON: %v", i, err)
			}
		}
	}

	// Verify audit-events.jsonl is present (may be empty since no auditStreamer)
	if _, ok := files["audit-events.jsonl"]; !ok {
		t.Error("audit-events.jsonl not found in ZIP")
	}

	// Verify README.md contains key fields
	if f, ok := files["README.md"]; ok {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		readme := string(data)

		checks := []string{
			"Full lifecycle test",
			string(reliability.SeverityP2),
			"Memory leak in worker pool",
			"Deployed hotfix v1.2.3",
			"High memory alert",
			"Timeline",
		}
		for _, s := range checks {
			if !contains(readme, s) {
				t.Errorf("README.md missing expected string: %q", s)
			}
		}
	}
}

func TestGeneratePostmortemBundle_WithAuditStreamer(t *testing.T) {
	store := tempIncidentStore(t)
	created := mustCreate(t, store, reliability.Incident{
		Title:    "Audit stream test",
		Severity: reliability.SeverityP3,
	})
	inc, _, _ := store.Get(created.ID)
	timeline, _ := store.GetTimeline(created.ID)

	// Inject a fake audit streamer
	fakeAuditJSON := `{"id":"audit-1","type":"command.sent","summary":"test command"}` + "\n"
	auditStreamer := func(w io.Writer) error {
		_, err := io.WriteString(w, fakeAuditJSON)
		return err
	}

	var buf bytes.Buffer
	if err := reliability.GeneratePostmortemBundle(&buf, inc, timeline, auditStreamer); err != nil {
		t.Fatalf("GeneratePostmortemBundle: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	for _, f := range zr.File {
		if f.Name == "audit-events.jsonl" {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			_ = rc.Close()
			if string(data) != fakeAuditJSON {
				t.Errorf("audit-events.jsonl content mismatch: got %q", string(data))
			}
			return
		}
	}
	t.Error("audit-events.jsonl not found")
}

// ── Filter combinations ───────────────────────────────────────

func TestIncidentStore_List_FilterCombined(t *testing.T) {
	store := tempIncidentStore(t)

	base := time.Now().UTC()
	mustCreate(t, store, reliability.Incident{
		Title:          "Combined test 1",
		Severity:       reliability.SeverityP1,
		Status:         reliability.StatusOpen,
		AffectedProbes: []string{"probe-alpha"},
		StartTime:      base.Add(-30 * time.Minute),
	})
	mustCreate(t, store, reliability.Incident{
		Title:          "Combined test 2",
		Severity:       reliability.SeverityP1,
		Status:         reliability.StatusInvestigating,
		AffectedProbes: []string{"probe-beta"},
		StartTime:      base.Add(-15 * time.Minute),
	})
	mustCreate(t, store, reliability.Incident{
		Title:          "Combined test 3",
		Severity:       reliability.SeverityP2,
		AffectedProbes: []string{"probe-alpha"},
		StartTime:      base,
	})

	// P1 + probe-alpha: should return 1
	list, err := store.List(reliability.IncidentFilter{
		Severity: reliability.SeverityP1,
		Probe:    "probe-alpha",
	})
	if err != nil {
		t.Fatalf("List combined: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1, got %d", len(list))
	}

	// Date range + severity: incidents in the last 20 min with P1
	list2, err := store.List(reliability.IncidentFilter{
		Severity: reliability.SeverityP1,
		From:     base.Add(-20 * time.Minute),
	})
	if err != nil {
		t.Fatalf("List date+severity: %v", err)
	}
	if len(list2) != 1 {
		t.Errorf("expected 1, got %d", len(list2))
	}
	if list2[0].Title != "Combined test 2" {
		t.Errorf("unexpected title: %s", list2[0].Title)
	}
}

// ── helpers ───────────────────────────────────────────────────

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range splitLines(s) {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		findString(s, sub))
}

func findString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
