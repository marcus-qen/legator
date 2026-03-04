package compliance

import (
	"bytes"
	"encoding/csv"
	"testing"
	"time"

	"go.uber.org/zap"
)

// mockClock allows controlling time in scheduler tests.
type mockClock struct {
	now time.Time
}

func (m *mockClock) Now() time.Time                         { return m.now }
func (m *mockClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func TestStoreExports_SaveAndGet(t *testing.T) {
	store := makeTestStore(t)

	rec := ExportRecord{
		Format:    ExportFormatCSV,
		CreatedAt: time.Now().UTC(),
		Status:    "ok",
		ProbeIDs:  []string{"probe-1"},
		Category:  "ssh",
	}
	data := []byte("probe_id,check_id\nprobe-1,ssh-pass\n")

	if err := store.SaveExport(rec, data); err != nil {
		t.Fatalf("SaveExport: %v", err)
	}

	// ID is assigned during Save
	exports, err := store.ListExports(10)
	if err != nil {
		t.Fatalf("ListExports: %v", err)
	}
	if len(exports) != 1 {
		t.Fatalf("expected 1 export, got %d", len(exports))
	}

	got, gotData, err := store.GetExport(exports[0].ID)
	if err != nil {
		t.Fatalf("GetExport: %v", err)
	}
	if got.Format != ExportFormatCSV {
		t.Errorf("format: want csv got %s", got.Format)
	}
	if got.Status != "ok" {
		t.Errorf("status: want ok got %s", got.Status)
	}
	if got.Category != "ssh" {
		t.Errorf("category: want ssh got %s", got.Category)
	}
	if len(got.ProbeIDs) != 1 || got.ProbeIDs[0] != "probe-1" {
		t.Errorf("probe_ids: want [probe-1] got %v", got.ProbeIDs)
	}
	if string(gotData) != string(data) {
		t.Errorf("data mismatch: want %q got %q", data, gotData)
	}
}

func TestStoreExports_GetNotFound(t *testing.T) {
	store := makeTestStore(t)
	_, _, err := store.GetExport("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent export, got nil")
	}
}

func TestStoreExports_PurgeOld(t *testing.T) {
	store := makeTestStore(t)

	old := ExportRecord{
		Format:    ExportFormatCSV,
		CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
		Status:    "ok",
	}
	recent := ExportRecord{
		Format:    ExportFormatCSV,
		CreatedAt: time.Now().UTC(),
		Status:    "ok",
	}

	if err := store.SaveExport(old, []byte("old")); err != nil {
		t.Fatalf("SaveExport old: %v", err)
	}
	if err := store.SaveExport(recent, []byte("recent")); err != nil {
		t.Fatalf("SaveExport recent: %v", err)
	}

	n, err := store.PurgeOldExports(24 * time.Hour)
	if err != nil {
		t.Fatalf("PurgeOldExports: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 purged row, got %d", n)
	}

	remaining, err := store.ListExports(10)
	if err != nil {
		t.Fatalf("ListExports: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining export, got %d", len(remaining))
	}
}

func TestScheduler_GenerateOnDemandCSV(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)

	id, err := sched.GenerateOnDemand(ExportFormatCSV, ExportFilter{})
	if err != nil {
		t.Fatalf("GenerateOnDemand CSV: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty export ID")
	}

	rec, data, err := store.GetExport(id)
	if err != nil {
		t.Fatalf("GetExport: %v", err)
	}
	if rec.Format != ExportFormatCSV {
		t.Errorf("format: want csv got %s", rec.Format)
	}
	if rec.Status != "ok" {
		t.Errorf("status: want ok got %s", rec.Status)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty CSV data")
	}

	// Verify it's valid CSV with correct columns
	r := csv.NewReader(bytes.NewReader(data))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("stored CSV is invalid: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("CSV has no rows")
	}
}

func TestScheduler_GenerateOnDemandPDF(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)

	id, err := sched.GenerateOnDemand(ExportFormatPDF, ExportFilter{})
	if err != nil {
		t.Fatalf("GenerateOnDemand PDF: %v", err)
	}

	_, data, err := store.GetExport(id)
	if err != nil {
		t.Fatalf("GetExport: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatal("stored PDF does not start with PDF header")
	}
}

func TestScheduler_GenerateOnDemandInvalidFormat(t *testing.T) {
	store := makeTestStore(t)
	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)

	_, err := sched.GenerateOnDemand("docx", ExportFilter{})
	if err == nil {
		t.Fatal("expected error for unknown format, got nil")
	}
}

func TestScheduler_WithMockClock(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	logger, _ := zap.NewDevelopment()
	clk := &mockClock{now: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)}
	sched := newSchedulerWithClock(store, "test-fleet", logger, clk)

	id, err := sched.GenerateOnDemand(ExportFormatCSV, ExportFilter{})
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}

	rec, _, err := store.GetExport(id)
	if err != nil {
		t.Fatalf("GetExport: %v", err)
	}

	if !rec.CreatedAt.Equal(clk.now) {
		t.Errorf("created_at: want %v got %v", clk.now, rec.CreatedAt)
	}
}

func TestScheduler_IsDueNoExports(t *testing.T) {
	store := makeTestStore(t)
	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)

	entry := ScheduleEntry{
		ID:       "daily-csv",
		Name:     "Daily CSV",
		Format:   ExportFormatCSV,
		Interval: ScheduleDaily,
		Enabled:  true,
	}

	due, err := sched.isDue(entry, time.Now())
	if err != nil {
		t.Fatalf("isDue: %v", err)
	}
	if !due {
		t.Fatal("expected schedule to be due when no exports exist")
	}
}

func TestScheduler_IsDueWithRecentExport(t *testing.T) {
	store := makeTestStore(t)
	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)

	// Store a recent export
	rec := ExportRecord{
		Format:    ExportFormatCSV,
		CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
		Status:    "ok",
	}
	if err := store.SaveExport(rec, []byte("data")); err != nil {
		t.Fatalf("SaveExport: %v", err)
	}

	entry := ScheduleEntry{
		ID:       "daily-csv",
		Format:   ExportFormatCSV,
		Interval: ScheduleDaily,
		Enabled:  true,
	}

	due, err := sched.isDue(entry, time.Now())
	if err != nil {
		t.Fatalf("isDue: %v", err)
	}
	if due {
		t.Fatal("expected schedule NOT to be due within the period")
	}
}

func TestScheduler_AddScheduleAndTick(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)
	sched.AddSchedule(ScheduleEntry{
		ID:       "weekly-csv",
		Name:     "Weekly CSV",
		Format:   ExportFormatCSV,
		Interval: ScheduleWeekly,
		Enabled:  true,
	})

	// Manually trigger a tick
	sched.tick(nil)

	// An export should have been created
	exports, err := store.ListExports(10)
	if err != nil {
		t.Fatalf("ListExports: %v", err)
	}
	if len(exports) == 0 {
		t.Fatal("expected at least one export after tick")
	}
}

func TestExportRecordToMap(t *testing.T) {
	rec := ExportRecord{
		ID:        "abc-123",
		Format:    ExportFormatPDF,
		Status:    "ok",
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		SizeBytes: 1024,
		Category:  "ssh",
		ProbeIDs:  []string{"p1", "p2"},
	}

	m := exportRecordToMap(rec)
	if m["id"] != "abc-123" {
		t.Errorf("id: want abc-123 got %v", m["id"])
	}
	if m["format"] != ExportFormatPDF {
		t.Errorf("format: want pdf got %v", m["format"])
	}
	if m["status"] != "ok" {
		t.Errorf("status: want ok got %v", m["status"])
	}
	if m["size_bytes"] != int64(1024) {
		t.Errorf("size_bytes: want 1024 got %v", m["size_bytes"])
	}
}
