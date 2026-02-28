package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
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
		JobID         string   `json:"job_id"`
		Runs          []JobRun `json:"runs"`
		Count         int      `json:"count"`
		FailedCount   int      `json:"failed_count"`
		SuccessCount  int      `json:"success_count"`
		RunningCount  int      `json:"running_count"`
		PendingCount  int      `json:"pending_count"`
		CanceledCount int      `json:"canceled_count"`
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
	if payload.FailedCount != 1 || payload.SuccessCount != 0 || payload.RunningCount != 0 || payload.PendingCount != 0 || payload.CanceledCount != 0 {
		t.Fatalf("unexpected summary failed=%d success=%d running=%d pending=%d canceled=%d", payload.FailedCount, payload.SuccessCount, payload.RunningCount, payload.PendingCount, payload.CanceledCount)
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

func TestHandleCancelRunAndTransitionConflict(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error { return nil }}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())
	h := NewHandler(store, scheduler)

	job, err := store.CreateJob(Job{
		Name:     "cancel-test",
		Command:  "echo hello",
		Schedule: "1h",
		Target:   Target{Kind: TargetKindProbe, Value: "probe-1"},
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	var run JobRun
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListRunsByJob(job.ID, 5)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) > 0 {
			run = runs[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.ID == "" {
		t.Fatal("expected run to be created")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/runs/"+run.ID+"/cancel", nil)
	req.SetPathValue("id", job.ID)
	req.SetPathValue("runId", run.ID)
	rr := httptest.NewRecorder()
	h.HandleCancelRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 cancel, got %d body=%s", rr.Code, rr.Body.String())
	}

	updated, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updated.Status != RunStatusCanceled {
		t.Fatalf("expected canceled status, got %s", updated.Status)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/runs/"+run.ID+"/cancel", nil)
	req2.SetPathValue("id", job.ID)
	req2.SetPathValue("runId", run.ID)
	rr2 := httptest.NewRecorder()
	h.HandleCancelRun(rr2, req2)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("expected 409 on terminal run cancel, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "invalid") {
		t.Fatalf("expected invalid transition response body, got %s", rr2.Body.String())
	}
}

func TestHandleCancelJobCancelsActiveRuns(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	fleetMgr := fleet.NewManager(zap.NewNop())
	fleetMgr.Register("probe-1", "probe-1", "linux", "amd64")
	if err := fleetMgr.SetOnline("probe-1"); err != nil {
		t.Fatalf("set online: %v", err)
	}

	tracker := newFakeTracker()
	sender := &fakeSender{sendFn: func(probeID string, msgType protocol.MessageType, payload any) error { return nil }}
	scheduler := NewScheduler(store, sender, fleetMgr, tracker, zap.NewNop())
	h := NewHandler(store, scheduler)

	job, err := store.CreateJob(Job{Name: "job-cancel", Command: "echo x", Schedule: "1h", Target: Target{Kind: TargetKindProbe, Value: "probe-1"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := scheduler.TriggerNow(job.ID); err != nil {
		t.Fatalf("trigger now: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/cancel", nil)
	req.SetPathValue("id", job.ID)
	rr := httptest.NewRecorder()
	h.HandleCancelJob(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 cancel job, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload struct {
		CanceledRuns int `json:"canceled_runs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.CanceledRuns < 1 {
		t.Fatalf("expected at least one canceled run, got %d", payload.CanceledRuns)
	}
}
