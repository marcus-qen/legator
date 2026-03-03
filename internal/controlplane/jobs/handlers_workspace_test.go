package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleCreateJobAssignsWorkspaceFromContext(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	h := NewHandler(store, nil)
	body := `{"name":"workspace-job","command":"echo ok","schedule":"@hourly","target":{"kind":"all"},"enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))
	req = req.WithContext(WithWorkspaceScope(context.Background(), "ws-a"))
	rr := httptest.NewRecorder()

	h.HandleCreateJob(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rr.Code, rr.Body.String())
	}

	var created Job
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.WorkspaceID != "ws-a" {
		t.Fatalf("expected workspace ws-a, got %q", created.WorkspaceID)
	}
}

func TestHandleDeleteJobRejectsCrossWorkspace(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	job, err := store.CreateJob(Job{
		WorkspaceID: "ws-a",
		Name:        "ws-a-job",
		Command:     "echo ok",
		Schedule:    "@hourly",
		Target:      Target{Kind: TargetKindAll},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	h := NewHandler(store, nil)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil)
	req.SetPathValue("id", job.ID)
	req = req.WithContext(WithWorkspaceScope(context.Background(), "ws-b"))
	rr := httptest.NewRecorder()
	h.HandleDeleteJob(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-workspace delete, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleListAllRunsWorkspaceFilter(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	jobA, err := store.CreateJob(Job{WorkspaceID: "ws-a", Name: "a", Command: "echo a", Schedule: "@hourly", Target: Target{Kind: TargetKindAll}, Enabled: true})
	if err != nil {
		t.Fatalf("create jobA: %v", err)
	}
	jobB, err := store.CreateJob(Job{WorkspaceID: "ws-b", Name: "b", Command: "echo b", Schedule: "@hourly", Target: Target{Kind: TargetKindAll}, Enabled: true})
	if err != nil {
		t.Fatalf("create jobB: %v", err)
	}
	if _, err := store.RecordRunStart(JobRun{JobID: jobA.ID, ProbeID: "p-a", RequestID: "run-a", Status: RunStatusRunning}); err != nil {
		t.Fatalf("record run a: %v", err)
	}
	if _, err := store.RecordRunStart(JobRun{JobID: jobB.ID, ProbeID: "p-b", RequestID: "run-b", Status: RunStatusRunning}); err != nil {
		t.Fatalf("record run b: %v", err)
	}

	h := NewHandler(store, nil)

	wsReq := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs", nil)
	wsReq = wsReq.WithContext(WithWorkspaceScope(context.Background(), "ws-a"))
	wsRR := httptest.NewRecorder()
	h.HandleListAllRuns(wsRR, wsReq)
	if wsRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", wsRR.Code, wsRR.Body.String())
	}
	var wsPayload struct {
		Runs []JobRun `json:"runs"`
	}
	if err := json.Unmarshal(wsRR.Body.Bytes(), &wsPayload); err != nil {
		t.Fatalf("decode ws response: %v", err)
	}
	if len(wsPayload.Runs) != 1 || wsPayload.Runs[0].RequestID != "run-a" {
		t.Fatalf("expected only ws-a run, got %+v", wsPayload.Runs)
	}

	allReq := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs", nil)
	allReq = allReq.WithContext(WithWorkspaceScope(context.Background(), ""))
	allRR := httptest.NewRecorder()
	h.HandleListAllRuns(allRR, allReq)
	if allRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", allRR.Code, allRR.Body.String())
	}
	var allPayload struct {
		Runs []JobRun `json:"runs"`
	}
	if err := json.Unmarshal(allRR.Body.Bytes(), &allPayload); err != nil {
		t.Fatalf("decode all response: %v", err)
	}
	if len(allPayload.Runs) != 2 {
		t.Fatalf("expected 2 runs without workspace filter, got %d", len(allPayload.Runs))
	}
}
