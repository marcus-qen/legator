package discovery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/marcus-qen/legator/internal/protocol"
)

func newTestCandidateHandler(t *testing.T) *CandidateHandler {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "disc.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cs, err := store.OpenCandidateStore()
	if err != nil {
		t.Fatalf("open candidate store: %v", err)
	}
	return NewCandidateHandler(cs)
}

func seedCandidate(t *testing.T, h *CandidateHandler, ip string) *DeployCandidate {
	t.Helper()
	payload := protocol.DiscoveryReportPayload{
		ProbeID: "probe-seed",
		Hosts: []protocol.DiscoveredHost{
			{IP: ip, Port: 22, SSHBanner: "SSH-2.0-OpenSSH_8.4"},
		},
		ScannedAt: time.Now().UTC(),
	}
	if err := h.HandleDiscoveryReport("probe-seed", payload); err != nil {
		t.Fatalf("seed discovery report: %v", err)
	}
	c, err := h.store.GetByIPPort(ip, 22)
	if err != nil {
		t.Fatalf("get seeded candidate: %v", err)
	}
	return c
}

func TestCandidateHandlerListEmpty(t *testing.T) {
	h := newTestCandidateHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/candidates", nil)
	rec := httptest.NewRecorder()
	h.HandleListCandidates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	candidates := resp["candidates"].([]any)
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(candidates))
	}
}

func TestCandidateHandlerListAfterReport(t *testing.T) {
	h := newTestCandidateHandler(t)
	seedCandidate(t, h, "10.0.0.1")
	seedCandidate(t, h, "10.0.0.2")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/candidates", nil)
	rec := httptest.NewRecorder()
	h.HandleListCandidates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)

	total := int(resp["total"].(float64))
	if total != 2 {
		t.Fatalf("expected total 2, got %d", total)
	}
}

func TestCandidateHandlerListFilterByStatus(t *testing.T) {
	h := newTestCandidateHandler(t)
	c := seedCandidate(t, h, "10.0.0.3")
	_ = h.store.Transition(c.ID, CandidateStatusApproved, "")

	// Filter: approved
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/candidates?status=approved", nil)
	rec := httptest.NewRecorder()
	h.HandleListCandidates(rec, req)

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if int(resp["total"].(float64)) != 1 {
		t.Fatalf("expected 1 approved, got %v", resp["total"])
	}

	// Filter: discovered (should be 0 after approval)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/candidates?status=discovered", nil)
	rec2 := httptest.NewRecorder()
	h.HandleListCandidates(rec2, req2)
	var resp2 map[string]any
	json.NewDecoder(rec2.Body).Decode(&resp2)
	if int(resp2["total"].(float64)) != 0 {
		t.Fatalf("expected 0 discovered after approval")
	}
}

func TestCandidateHandlerGetCandidate(t *testing.T) {
	h := newTestCandidateHandler(t)
	c := seedCandidate(t, h, "10.0.0.4")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/candidates/"+c.ID, nil)
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	h.HandleGetCandidate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got DeployCandidate
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IP != "10.0.0.4" {
		t.Fatalf("expected IP 10.0.0.4, got %q", got.IP)
	}
}

func TestCandidateHandlerGetNotFound(t *testing.T) {
	h := newTestCandidateHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/candidates/no-such-id", nil)
	req.SetPathValue("id", "no-such-id")
	rec := httptest.NewRecorder()
	h.HandleGetCandidate(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCandidateHandlerApprove(t *testing.T) {
	h := newTestCandidateHandler(t)
	c := seedCandidate(t, h, "10.0.0.5")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/discovery/candidates/"+c.ID+"/approve",
		bytes.NewReader(nil))
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	h.HandleApproveCandidate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got DeployCandidate
	json.NewDecoder(rec.Body).Decode(&got)
	if got.Status != CandidateStatusApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}
}

func TestCandidateHandlerReject(t *testing.T) {
	h := newTestCandidateHandler(t)
	c := seedCandidate(t, h, "10.0.0.6")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/discovery/candidates/"+c.ID+"/reject",
		bytes.NewReader(nil))
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	h.HandleRejectCandidate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got DeployCandidate
	json.NewDecoder(rec.Body).Decode(&got)
	if got.Status != CandidateStatusRejected {
		t.Fatalf("expected rejected, got %q", got.Status)
	}
}

func TestCandidateHandlerApproveInvalidTransition(t *testing.T) {
	h := newTestCandidateHandler(t)
	c := seedCandidate(t, h, "10.0.0.7")
	// Move to rejected first
	_ = h.store.Transition(c.ID, CandidateStatusRejected, "")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/discovery/candidates/"+c.ID+"/approve",
		bytes.NewReader(nil))
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	h.HandleApproveCandidate(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCandidateHandlerDiscoveryReport(t *testing.T) {
	h := newTestCandidateHandler(t)

	payload := protocol.DiscoveryReportPayload{
		ProbeID: "probe-abc",
		Hosts: []protocol.DiscoveredHost{
			{IP: "192.168.1.10", Port: 22, SSHBanner: "SSH-2.0-OpenSSH_8.9", OSGuess: "linux"},
			{IP: "192.168.1.11", Port: 22, SSHBanner: "SSH-2.0-Dropbear"},
		},
		ScannedAt: time.Now().UTC(),
	}

	if err := h.HandleDiscoveryReport("probe-abc", payload); err != nil {
		t.Fatalf("handle discovery report: %v", err)
	}

	all, err := h.store.List("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(all))
	}
	if all[0].SourceProbe != "probe-abc" && all[1].SourceProbe != "probe-abc" {
		t.Fatal("expected source probe to be probe-abc")
	}
}

func TestCandidateHandlerDeployResult(t *testing.T) {
	h := newTestCandidateHandler(t)
	c := seedCandidate(t, h, "10.0.0.10")
	_ = h.store.Transition(c.ID, CandidateStatusApproved, "")
	_ = h.store.Transition(c.ID, CandidateStatusDeploying, "")

	// Successful deploy result
	result := protocol.DeployResultPayload{
		RequestID:   "req-1",
		CandidateID: c.ID,
		Success:     true,
	}
	if err := h.HandleDeployResult("probe-abc", result); err != nil {
		t.Fatalf("handle deploy result: %v", err)
	}
	got, _ := h.store.Get(c.ID)
	if got.Status != CandidateStatusDeployed {
		t.Fatalf("expected deployed, got %q", got.Status)
	}
}

func TestCandidateHandlerDeployResultFailure(t *testing.T) {
	h := newTestCandidateHandler(t)
	c := seedCandidate(t, h, "10.0.0.11")
	_ = h.store.Transition(c.ID, CandidateStatusApproved, "")
	_ = h.store.Transition(c.ID, CandidateStatusDeploying, "")

	result := protocol.DeployResultPayload{
		RequestID:   "req-2",
		CandidateID: c.ID,
		Success:     false,
		Error:       "connection refused",
	}
	if err := h.HandleDeployResult("probe-abc", result); err != nil {
		t.Fatalf("handle failed deploy result: %v", err)
	}
	got, _ := h.store.Get(c.ID)
	if got.Status != CandidateStatusFailed {
		t.Fatalf("expected failed, got %q", got.Status)
	}
	if got.Error != "connection refused" {
		t.Fatalf("expected error 'connection refused', got %q", got.Error)
	}
}

func TestCandidateHandlerNilStore(t *testing.T) {
	h := &CandidateHandler{store: nil}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/candidates", nil)
	rec := httptest.NewRecorder()
	h.HandleListCandidates(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
