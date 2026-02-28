package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHandleListRunsSupportsFiltersAndSummary(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	job, err := store.CreateJob(Job{
		Name:     "nightly",
		Command:  "echo ok",
		Schedule: "5m",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	base := time.Now().UTC().Add(-2 * time.Minute)
	successRun, err := store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-1", RequestID: "run-success", StartedAt: base})
	if err != nil {
		t.Fatalf("record success run: %v", err)
	}
	if err := store.CompleteRun(successRun.ID, RunStatusSuccess, intPtr(0), "ok"); err != nil {
		t.Fatalf("complete success run: %v", err)
	}

	failedRun, err := store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-2", RequestID: "run-failed", StartedAt: base.Add(30 * time.Second)})
	if err != nil {
		t.Fatalf("record failed run: %v", err)
	}
	if err := store.CompleteRun(failedRun.ID, RunStatusFailed, intPtr(2), "boom"); err != nil {
		t.Fatalf("complete failed run: %v", err)
	}

	_, err = store.RecordRunStart(JobRun{JobID: job.ID, ProbeID: "probe-3", RequestID: "run-running", StartedAt: base.Add(time.Minute)})
	if err != nil {
		t.Fatalf("record running run: %v", err)
	}

	h := NewHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+job.ID+"/runs?status=failed&limit=1", nil)
	req.SetPathValue("id", job.ID)
	rr := httptest.NewRecorder()
	h.HandleListRuns(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload struct {
		JobID        string   `json:"job_id"`
		Runs         []JobRun `json:"runs"`
		Count        int      `json:"count"`
		FailedCount  int      `json:"failed_count"`
		SuccessCount int      `json:"success_count"`
		RunningCount int      `json:"running_count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if payload.JobID != job.ID {
		t.Fatalf("expected job_id=%s, got %s", job.ID, payload.JobID)
	}
	if payload.Count != 1 || len(payload.Runs) != 1 {
		t.Fatalf("expected one failed run, count=%d len=%d", payload.Count, len(payload.Runs))
	}
	if payload.Runs[0].Status != RunStatusFailed {
		t.Fatalf("expected failed status, got %s", payload.Runs[0].Status)
	}
	if payload.FailedCount != 1 || payload.SuccessCount != 0 || payload.RunningCount != 0 {
		t.Fatalf("unexpected summary failed=%d success=%d running=%d", payload.FailedCount, payload.SuccessCount, payload.RunningCount)
	}
}

func TestHandleListAllRunsSupportsJobFilter(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	jobA, err := store.CreateJob(Job{Name: "A", Command: "echo A", Schedule: "1m", Target: Target{Kind: TargetKindProbe, Value: "probe-a"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job A: %v", err)
	}
	jobB, err := store.CreateJob(Job{Name: "B", Command: "echo B", Schedule: "1m", Target: Target{Kind: TargetKindProbe, Value: "probe-b"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job B: %v", err)
	}

	runA, err := store.RecordRunStart(JobRun{JobID: jobA.ID, ProbeID: "probe-a", RequestID: "job-a-failed", StartedAt: time.Now().UTC().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("record jobA run: %v", err)
	}
	if err := store.CompleteRun(runA.ID, RunStatusFailed, intPtr(1), "failed"); err != nil {
		t.Fatalf("complete jobA run: %v", err)
	}

	runB, err := store.RecordRunStart(JobRun{JobID: jobB.ID, ProbeID: "probe-b", RequestID: "job-b-failed", StartedAt: time.Now().UTC().Add(-30 * time.Second)})
	if err != nil {
		t.Fatalf("record jobB run: %v", err)
	}
	if err := store.CompleteRun(runB.ID, RunStatusFailed, intPtr(3), "failed"); err != nil {
		t.Fatalf("complete jobB run: %v", err)
	}

	h := NewHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs?status=failed&job_id="+jobA.ID, nil)
	rr := httptest.NewRecorder()
	h.HandleListAllRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Runs  []JobRun `json:"runs"`
		Count int      `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Count != 1 || len(payload.Runs) != 1 {
		t.Fatalf("expected one filtered run, count=%d len=%d", payload.Count, len(payload.Runs))
	}
	if payload.Runs[0].JobID != jobA.ID {
		t.Fatalf("expected job_id %s, got %s", jobA.ID, payload.Runs[0].JobID)
	}
}
