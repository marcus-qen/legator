package compliance

import (
	"bytes"
	"testing"
)

func TestWritePDF_EmptyStore(t *testing.T) {
	store := makeTestStore(t)
	var buf bytes.Buffer
	if err := WritePDF(store, ExportFilter{}, "all probes", &buf); err != nil {
		t.Fatalf("WritePDF on empty store: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty PDF output")
	}
	// Verify PDF magic bytes
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Fatalf("output does not start with PDF header, got: %q", buf.Bytes()[:min(20, buf.Len())])
	}
}

func TestWritePDF_WithResults(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	var buf bytes.Buffer
	if err := WritePDF(store, ExportFilter{}, "test-fleet", &buf); err != nil {
		t.Fatalf("WritePDF: %v", err)
	}

	if buf.Len() < 1000 {
		t.Fatalf("PDF too small (%d bytes), likely broken", buf.Len())
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Fatal("output does not start with PDF header")
	}
}

func TestWritePDF_FilterByCategory(t *testing.T) {
	store := makeTestStore(t)
	seedTestResults(t, store)

	var buf bytes.Buffer
	if err := WritePDF(store, ExportFilter{Category: "ssh"}, "all probes", &buf); err != nil {
		t.Fatalf("WritePDF with category filter: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty PDF output")
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Fatal("output does not start with PDF header")
	}
}

func TestWritePDF_SpecialCharactersInEvidence(t *testing.T) {
	store := makeTestStore(t)
	results := seedTestResults(t, store)
	_ = results

	// Insert a result with long evidence that triggers truncation
	r := ComplianceResult{
		CheckID:   "long-evidence",
		CheckName: "Long Evidence Check",
		Category:  "security",
		Severity:  SeverityCritical,
		ProbeID:   "probe-x",
		Status:    StatusFail,
		Evidence:  "This is a very long evidence string that exceeds the 60 character display limit for evidence in the detail table",
	}
	if err := store.UpsertResult(r); err != nil {
		t.Fatalf("UpsertResult: %v", err)
	}

	var buf bytes.Buffer
	if err := WritePDF(store, ExportFilter{}, "all probes", &buf); err != nil {
		t.Fatalf("WritePDF with long evidence: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty PDF")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
