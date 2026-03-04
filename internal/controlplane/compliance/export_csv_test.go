package compliance

import (
	"bytes"
	"encoding/csv"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedTestResults(t *testing.T, store *Store) []ComplianceResult {
	t.Helper()
	ts1 := time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 1, 11, 9, 0, 0, 0, time.UTC)

	results := []ComplianceResult{
		{CheckID: "ssh-pass", CheckName: "SSH Password Auth", Category: "ssh", Severity: SeverityHigh, ProbeID: "probe-1", Status: StatusPass, Evidence: "disabled", Timestamp: ts1},
		{CheckID: "fw-active", CheckName: "Firewall Active", Category: "firewall", Severity: SeverityHigh, ProbeID: "probe-1", Status: StatusFail, Evidence: "no firewall", Timestamp: ts1},
		{CheckID: "ssh-pass", CheckName: "SSH Password Auth", Category: "ssh", Severity: SeverityHigh, ProbeID: "probe-2", Status: StatusWarning, Evidence: "partial", Timestamp: ts2},
		{CheckID: "updates", CheckName: "Auto Updates", Category: "patching", Severity: SeverityMedium, ProbeID: "probe-2", Status: StatusPass, Evidence: "enabled", Timestamp: ts2},
	}
	for _, r := range results {
		if err := store.UpsertResult(r); err != nil {
			t.Fatalf("UpsertResult: %v", err)
		}
	}
	return results
}

func TestWriteCSV_Header(t *testing.T) {
	store := makeTestStore(t)
	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{}, &buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected at least a header row")
	}
	header := records[0]
	expected := csvColumns
	if len(header) != len(expected) {
		t.Fatalf("expected %d columns, got %d: %v", len(expected), len(header), header)
	}
	for i, col := range expected {
		if header[i] != col {
			t.Errorf("column %d: want %q got %q", i, col, header[i])
		}
	}
}

func TestWriteCSV_AllResults(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{}, &buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	// header + 4 data rows
	if len(records) != 5 {
		t.Fatalf("expected 5 rows (1 header + 4 data), got %d", len(records))
	}
}

func TestWriteCSV_FilterByCategory(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{Category: "ssh"}, &buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	// header + 2 ssh rows
	if len(records) != 3 {
		t.Fatalf("expected 3 rows for category=ssh, got %d", len(records))
	}
	// Verify all data rows are ssh category (column index 3)
	for _, row := range records[1:] {
		if row[3] != "ssh" {
			t.Errorf("expected category=ssh, got %q", row[3])
		}
	}
}

func TestWriteCSV_FilterByProbe(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{ProbeIDs: []string{"probe-1"}}, &buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	if len(records) != 3 { // header + 2 for probe-1
		t.Fatalf("expected 3 rows for probe-1, got %d", len(records))
	}
}

func TestWriteCSV_FilterByMultipleProbes(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{ProbeIDs: []string{"probe-1", "probe-2"}}, &buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("expected 5 rows for 2 probes, got %d", len(records))
	}
}

func TestWriteCSV_FilterBySince(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	since := time.Date(2024, 1, 11, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{Since: since}, &buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	// Only ts2 results (2 records)
	if len(records) != 3 { // header + 2
		t.Fatalf("expected 3 rows after since filter, got %d", len(records))
	}
}

func TestWriteCSV_EmptyStore(t *testing.T) {
	store := makeTestStore(t)
	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{}, &buf); err != nil {
		t.Fatalf("WriteCSV on empty store: %v", err)
	}
	// Should produce header only
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (header only), got %d", len(lines))
	}
}

func TestWriteCSV_ColumnsContent(t *testing.T) {
	store := makeTestStore(t)
	ts := time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)
	r := ComplianceResult{
		CheckID:   "test-check",
		CheckName: "Test Check",
		Category:  "security",
		Severity:  SeverityCritical,
		ProbeID:   "probe-abc",
		Status:    StatusFail,
		Evidence:  "test evidence",
		Timestamp: ts,
	}
	if err := store.UpsertResult(r); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}

	var buf bytes.Buffer
	if err := WriteCSV(store, ExportFilter{}, &buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	cr := csv.NewReader(&buf)
	records, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(records))
	}

	row := records[1]
	checks := map[int]string{
		0: "probe-abc",
		1: "test-check",
		2: "Test Check",
		3: "security",
		4: SeverityCritical,
		5: StatusFail,
		6: "test evidence",
		7: "2024-03-15T10:30:00Z",
	}
	for col, want := range checks {
		if row[col] != want {
			t.Errorf("col %d: want %q got %q", col, want, row[col])
		}
	}
}

func TestBuildExportFilterDescription(t *testing.T) {
	cases := []struct {
		filter ExportFilter
		want   string
	}{
		{ExportFilter{}, "all results"},
		{ExportFilter{Category: "ssh"}, "category: ssh"},
		{ExportFilter{ProbeIDs: []string{"p1"}}, "probes: p1"},
	}
	for _, tc := range cases {
		got := buildExportFilterDescription(tc.filter)
		if got != tc.want {
			t.Errorf("filter %+v: want %q got %q", tc.filter, tc.want, got)
		}
	}
}
