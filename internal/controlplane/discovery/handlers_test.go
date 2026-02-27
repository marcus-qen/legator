package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/api"
)

type scannerFunc func(ctx context.Context, cidr string, timeout time.Duration) ([]Candidate, error)

func (f scannerFunc) Scan(ctx context.Context, cidr string, timeout time.Duration) ([]Candidate, error) {
	return f(ctx, cidr, timeout)
}

func newTestHandler(t *testing.T, scanner ScannerAPI) *Handler {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "discovery.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewHandler(store, scanner, api.NewTokenStore())
}

func TestHandleScanPersistsRunAndCandidates(t *testing.T) {
	h := newTestHandler(t, scannerFunc(func(ctx context.Context, cidr string, timeout time.Duration) ([]Candidate, error) {
		if cidr != "192.168.1.0/24" {
			t.Fatalf("unexpected cidr: %s", cidr)
		}
		return []Candidate{
			{IP: "192.168.1.10", Hostname: "host-a", OpenPorts: []int{22}, Confidence: ConfidenceHigh},
			{IP: "192.168.1.11", OpenPorts: []int{80}, Confidence: ConfidenceMedium},
		}, nil
	}))

	body := map[string]any{"cidr": "192.168.1.0/24", "timeout_ms": 250}
	data, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/scan", bytes.NewReader(data))
	recorder := httptest.NewRecorder()

	h.HandleScan(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var response ScanResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Run.ID == 0 {
		t.Fatal("expected run id")
	}
	if response.Run.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %q", response.Run.Status)
	}
	if len(response.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(response.Candidates))
	}
}

func TestHandleScanRejectsInvalidCIDR(t *testing.T) {
	h := newTestHandler(t, scannerFunc(func(ctx context.Context, cidr string, timeout time.Duration) ([]Candidate, error) {
		return nil, nil
	}))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/scan", strings.NewReader(`{"cidr":"10.0.0.0/16"}`))
	recorder := httptest.NewRecorder()

	h.HandleScan(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

func TestHandleListRunsAndGetRun(t *testing.T) {
	h := newTestHandler(t, scannerFunc(func(ctx context.Context, cidr string, timeout time.Duration) ([]Candidate, error) {
		return []Candidate{{IP: "10.0.0.10", OpenPorts: []int{22}, Confidence: ConfidenceHigh}}, nil
	}))

	scanReq := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/scan", strings.NewReader(`{"cidr":"10.0.0.0/24"}`))
	scanRR := httptest.NewRecorder()
	h.HandleScan(scanRR, scanReq)
	if scanRR.Code != http.StatusOK {
		t.Fatalf("scan expected 200, got %d: %s", scanRR.Code, scanRR.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/runs?limit=5", nil)
	listRR := httptest.NewRecorder()
	h.HandleListRuns(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list expected 200, got %d", listRR.Code)
	}

	var listPayload struct {
		Runs []ScanRun `json:"runs"`
	}
	if err := json.NewDecoder(listRR.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list payload: %v", err)
	}
	if len(listPayload.Runs) == 0 {
		t.Fatal("expected at least one run")
	}

	runID := listPayload.Runs[0].ID
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/runs/1", nil)
	getReq.SetPathValue("id", strconv.FormatInt(runID, 10))
	getRR := httptest.NewRecorder()
	h.HandleGetRun(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("get expected 200, got %d: %s", getRR.Code, getRR.Body.String())
	}
}

func TestHandleInstallToken(t *testing.T) {
	h := newTestHandler(t, scannerFunc(func(ctx context.Context, cidr string, timeout time.Duration) ([]Candidate, error) {
		return nil, nil
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/install-token", nil)
	req.Host = "cp.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()

	h.HandleInstallToken(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp InstallTokenResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode install-token response: %v", err)
	}
	if !strings.HasPrefix(resp.Token, "prb_") {
		t.Fatalf("unexpected token format: %q", resp.Token)
	}
	if !strings.Contains(resp.InstallCommand, "--token "+resp.Token) {
		t.Fatalf("expected token in install command: %q", resp.InstallCommand)
	}
	if !strings.Contains(resp.SSHExampleTemplate, "ssh <user>@<ip>") {
		t.Fatalf("expected ssh placeholder template, got: %q", resp.SSHExampleTemplate)
	}
}
