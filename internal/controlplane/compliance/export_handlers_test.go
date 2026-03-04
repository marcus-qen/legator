package compliance

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

func makeTestExportHandler(t *testing.T) (*ExportHandler, *Store) {
	t.Helper()
	store := makeTestStore(t)
	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)
	return NewExportHandler(store, sched), store
}

func TestHandleExportCSV_OK(t *testing.T) {
	h, store := makeTestExportHandler(t)
	seedTestResults(t, store)

	req := httptest.NewRequest("GET", "/api/v1/compliance/export/csv", nil)
	w := httptest.NewRecorder()
	h.HandleExportCSV(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type: want text/csv got %q", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if len(cd) == 0 {
		t.Error("expected Content-Disposition header")
	}

	r := csv.NewReader(w.Body)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	// header + 4 data rows
	if len(records) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(records))
	}
}

func TestHandleExportCSV_WithFilter(t *testing.T) {
	h, store := makeTestExportHandler(t)
	seedTestResults(t, store)

	req := httptest.NewRequest("GET", "/api/v1/compliance/export/csv?category=ssh", nil)
	w := httptest.NewRecorder()
	h.HandleExportCSV(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	r := csv.NewReader(w.Body)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	if len(records) != 3 { // header + 2 ssh rows
		t.Fatalf("expected 3 rows for category=ssh, got %d", len(records))
	}
}

func TestHandleExportCSV_InvalidSince(t *testing.T) {
	h, _ := makeTestExportHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/compliance/export/csv?since=not-a-time", nil)
	w := httptest.NewRecorder()
	h.HandleExportCSV(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid since, got %d", resp.StatusCode)
	}
}

func TestHandleExportPDF_OK(t *testing.T) {
	h, store := makeTestExportHandler(t)
	seedTestResults(t, store)

	req := httptest.NewRequest("GET", "/api/v1/compliance/export/pdf", nil)
	w := httptest.NewRecorder()
	h.HandleExportPDF(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/pdf" {
		t.Errorf("content-type: want application/pdf got %q", ct)
	}
	if !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
		t.Fatal("response body does not start with PDF header")
	}
}

func TestHandleListExports_Empty(t *testing.T) {
	h, _ := makeTestExportHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/compliance/exports", nil)
	w := httptest.NewRecorder()
	h.HandleListExports(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["total"].(float64) != 0 {
		t.Errorf("expected total=0, got %v", body["total"])
	}
}

func TestHandleListExports_WithItems(t *testing.T) {
	h, store := makeTestExportHandler(t)
	// Save a couple of exports
	for _, fmt := range []ExportFormat{ExportFormatCSV, ExportFormatPDF} {
		rec := ExportRecord{
			Format:    fmt,
			CreatedAt: time.Now().UTC(),
			Status:    "ok",
		}
		if err := store.SaveExport(rec, []byte("data")); err != nil {
			t.Fatalf("SaveExport: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/compliance/exports", nil)
	w := httptest.NewRecorder()
	h.HandleListExports(w, req)

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["total"].(float64) != 2 {
		t.Errorf("expected total=2, got %v", body["total"])
	}
}

func TestHandleGetExport_NotFound(t *testing.T) {
	h, _ := makeTestExportHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/compliance/exports/missing-id", nil)
	req.SetPathValue("id", "missing-id")
	w := httptest.NewRecorder()
	h.HandleGetExport(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleGetExport_CSV(t *testing.T) {
	h, store := makeTestExportHandler(t)
	seedTestResults(t, store)
	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)

	id, err := sched.GenerateOnDemand(ExportFormatCSV, ExportFilter{})
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/compliance/exports/"+id, nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.HandleGetExport(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, w.Body.String())
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type: want text/csv got %q", ct)
	}
}

func TestHandleGetExport_PDF(t *testing.T) {
	h, store := makeTestExportHandler(t)
	seedTestResults(t, store)
	logger, _ := zap.NewDevelopment()
	sched := NewScheduler(store, "test-fleet", logger)

	id, err := sched.GenerateOnDemand(ExportFormatPDF, ExportFilter{})
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/compliance/exports/"+id, nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.HandleGetExport(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/pdf" {
		t.Errorf("content-type: want application/pdf got %q", ct)
	}
	if !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
		t.Fatal("response body does not look like a PDF")
	}
}

func TestParseExportFilter(t *testing.T) {
	req := httptest.NewRequest("GET",
		"/api/v1/compliance/export/csv?probes=p1,p2&category=ssh&since=2024-01-01T00:00:00Z&until=2024-12-31T23:59:59Z",
		nil)

	filter, err := parseExportFilter(req)
	if err != nil {
		t.Fatalf("parseExportFilter: %v", err)
	}
	if len(filter.ProbeIDs) != 2 {
		t.Errorf("probe_ids: want 2, got %d", len(filter.ProbeIDs))
	}
	if filter.Category != "ssh" {
		t.Errorf("category: want ssh got %q", filter.Category)
	}
	if filter.Since.IsZero() {
		t.Error("since: expected non-zero")
	}
	if filter.Until.IsZero() {
		t.Error("until: expected non-zero")
	}
}

func TestParseExportFilter_InvalidSince(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/compliance/export/csv?since=bad", nil)
	_, err := parseExportFilter(req)
	if err == nil {
		t.Fatal("expected error for invalid since")
	}
}

func TestParseExportFilter_InvalidUntil(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/compliance/export/csv?until=bad", nil)
	_, err := parseExportFilter(req)
	if err == nil {
		t.Fatal("expected error for invalid until")
	}
}
