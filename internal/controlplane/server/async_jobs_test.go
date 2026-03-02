package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/protocol"
)

func registerRemoteProbe(t *testing.T, srv *Server, id string) {
	t.Helper()
	_, err := srv.fleetMgr.RegisterRemote(fleet.RemoteProbeRegistration{
		ID:       id,
		Hostname: "remote-host",
		OS:       "linux",
		Arch:     "amd64",
		Remote: fleet.RemoteProbeConfig{
			Host:     "127.0.0.1",
			Port:     22,
			Username: "root",
			AuthMode: "password",
		},
		Credentials: fleet.RemoteProbeCredentials{Password: "secret"},
	})
	if err != nil {
		t.Fatalf("register remote probe: %v", err)
	}
}

func waitForAsyncJobState(t *testing.T, srv *Server, jobID string, allowed ...jobs.AsyncJobState) *jobs.AsyncJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := srv.asyncJobsManager.GetJob(jobID)
		if err == nil {
			for _, state := range allowed {
				if job.State == state {
					return job
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, err := srv.asyncJobsManager.GetJob(jobID)
	if err != nil {
		t.Fatalf("get async job after wait: %v", err)
	}
	t.Fatalf("job %s did not reach expected states %v, got %s", jobID, allowed, job.State)
	return nil
}

func hasApprovalTimeoutAudit(events []audit.Event, jobID, behavior, action string) bool {
	for _, evt := range events {
		detail, ok := evt.Detail.(map[string]any)
		if !ok {
			continue
		}
		if detail["job_id"] != jobID {
			continue
		}
		if detail["timeout_behavior"] == behavior && detail["timeout_action"] == action {
			return true
		}
	}
	return false
}

func createPendingApprovalAsyncJob(t *testing.T, srv *Server, probeID, requestID string) *jobs.AsyncJob {
	t.Helper()
	registerRemoteProbe(t, srv, probeID)
	srv.remoteExecutor = &fakeRemoteExecutor{}

	payload := protocol.CommandPayload{RequestID: requestID, Command: "systemctl", Args: []string{"restart", "nginx"}, Level: protocol.CapRemediate}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/"+probeID+"/command", bytes.NewReader(body))
	req.SetPathValue("id", probeID)
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202 pending approval, got %d body=%s", rr.Code, rr.Body.String())
	}

	jobID := rr.Header().Get("X-Legator-Job-ID")
	if jobID == "" {
		t.Fatalf("expected X-Legator-Job-ID header")
	}
	job, err := srv.asyncJobsManager.GetJob(jobID)
	if err != nil {
		t.Fatalf("get async job: %v", err)
	}
	if job.State != jobs.AsyncJobStateWaitingApproval {
		t.Fatalf("expected waiting_approval, got %s", job.State)
	}
	if job.ApprovalID == "" {
		t.Fatalf("expected approval id on waiting job")
	}
	return job
}

func TestHandleDispatchCommand_PersistsAsyncJobLifecycle(t *testing.T) {
	srv := newTestServer(t)
	registerRemoteProbe(t, srv, "remote-1")
	srv.remoteExecutor = &fakeRemoteExecutor{execResult: &protocol.CommandResultPayload{RequestID: "req-remote-success", ExitCode: 0, Stdout: "ok"}}

	payload := protocol.CommandPayload{RequestID: "req-remote-success", Command: "echo", Args: []string{"ok"}, Level: protocol.CapObserve}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/remote-1/command?wait=1", bytes.NewReader(body))
	req.SetPathValue("id", "remote-1")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	jobID := rr.Header().Get("X-Legator-Job-ID")
	if jobID == "" {
		t.Fatalf("expected X-Legator-Job-ID header")
	}
	job, err := srv.asyncJobsManager.GetJob(jobID)
	if err != nil {
		t.Fatalf("get async job: %v", err)
	}
	if job.State != jobs.AsyncJobStateSucceeded {
		t.Fatalf("expected succeeded state, got %s", job.State)
	}
}

func TestHandleDispatchCommand_PendingApprovalMarksAsyncJobWaiting(t *testing.T) {
	srv := newTestServer(t)
	_ = createPendingApprovalAsyncJob(t, srv, "remote-2", "req-remote-approval")
}

func TestHandleDispatchCommand_PendingApprovalHaltsAsyncDispatch(t *testing.T) {
	srv := newTestServer(t)
	registerRemoteProbe(t, srv, "remote-halt")
	exec := &fakeRemoteExecutor{}
	srv.remoteExecutor = exec

	payload := protocol.CommandPayload{RequestID: "req-remote-halt", Command: "systemctl", Args: []string{"restart", "nginx"}, Level: protocol.CapRemediate}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/remote-halt/command", bytes.NewReader(body))
	req.SetPathValue("id", "remote-halt")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202 pending approval, got %d body=%s", rr.Code, rr.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(exec.executed); got != 0 {
		t.Fatalf("expected no dispatch while pending approval, got %d executions", got)
	}
}

func TestHandleApproveAsyncJob_ResumesAndDispatches(t *testing.T) {
	srv := newTestServer(t)
	job := createPendingApprovalAsyncJob(t, srv, "remote-approve", "req-remote-approve")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/approve", bytes.NewBufferString(`{"decided_by":"operator"}`))
	req.SetPathValue("id", job.ID)
	rr := httptest.NewRecorder()

	srv.handleApproveAsyncJob(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	updated := waitForAsyncJobState(t, srv, job.ID, jobs.AsyncJobStateRunning, jobs.AsyncJobStateSucceeded)
	if updated.State == jobs.AsyncJobStateWaitingApproval {
		t.Fatalf("job should have resumed from waiting_approval")
	}
	if updated.ID != job.ID {
		t.Fatalf("expected same async job id after approve, got %s want %s", updated.ID, job.ID)
	}

	replay, err := srv.commandStreams.Replay(updated.RequestID, cmdtracker.StreamReplayQuery{Limit: 200})
	if err != nil {
		t.Fatalf("replay stream: %v", err)
	}
	foundApprovedBy := false
	for _, evt := range replay.Events {
		if evt.Kind != cmdtracker.StreamEventApproval || evt.Meta == nil {
			continue
		}
		if _, ok := evt.Meta["approved_by"]; ok {
			foundApprovedBy = true
			break
		}
	}
	if !foundApprovedBy {
		t.Fatalf("expected approval stream marker with approved_by")
	}
}

func TestHandleRejectAsyncJob_FailsWithReason(t *testing.T) {
	srv := newTestServer(t)
	job := createPendingApprovalAsyncJob(t, srv, "remote-reject", "req-remote-reject")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/reject", bytes.NewBufferString(`{"decided_by":"operator","reason":"unsafe command"}`))
	req.SetPathValue("id", job.ID)
	rr := httptest.NewRecorder()

	srv.handleRejectAsyncJob(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	failed := waitForAsyncJobState(t, srv, job.ID, jobs.AsyncJobStateFailed)
	if failed.StatusReason != "unsafe command" {
		t.Fatalf("expected rejection reason to be persisted, got %q", failed.StatusReason)
	}

	replay, err := srv.commandStreams.Replay(failed.RequestID, cmdtracker.StreamReplayQuery{Limit: 200})
	if err != nil {
		t.Fatalf("replay stream: %v", err)
	}
	foundRejectedBy := false
	for _, evt := range replay.Events {
		if evt.Kind != cmdtracker.StreamEventApproval || evt.Meta == nil {
			continue
		}
		if _, ok := evt.Meta["rejected_by"]; ok {
			foundRejectedBy = true
			break
		}
	}
	if !foundRejectedBy {
		t.Fatalf("expected approval stream marker with rejected_by")
	}
}

func TestProcessTimedOutApprovalJobs_CancelBehavior(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.Jobs.ApprovalTimeoutBehavior = "cancel"

	job, err := srv.asyncJobsManager.CreateForCommand("probe-timeout-cancel", protocol.CommandPayload{RequestID: "req-timeout-cancel", Command: "systemctl", Args: []string{"restart", "nginx"}, Level: protocol.CapRemediate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	approvalReq, err := srv.approvalQueue.Submit("probe-timeout-cancel", &protocol.CommandPayload{RequestID: job.RequestID, Command: job.Command, Args: job.Args, Level: protocol.CapRemediate}, "timeout test", "high", "tester")
	if err != nil {
		t.Fatalf("submit approval request: %v", err)
	}
	expiresAt := time.Now().UTC().Add(-time.Second)
	if _, err := srv.asyncJobsManager.MarkWaitingApproval(job.ID, approvalReq.ID, "waiting", &expiresAt); err != nil {
		t.Fatalf("mark waiting approval: %v", err)
	}

	processed, err := srv.processTimedOutApprovalJobs(time.Now().UTC())
	if err != nil {
		t.Fatalf("process timeout jobs: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 processed timeout job, got %d", processed)
	}

	updated := waitForAsyncJobState(t, srv, job.ID, jobs.AsyncJobStateCancelled)
	if updated.State != jobs.AsyncJobStateCancelled {
		t.Fatalf("expected cancelled state after timeout cancel, got %s", updated.State)
	}

	events := srv.queryAudit(audit.Filter{Limit: 100})
	if !hasApprovalTimeoutAudit(events, job.ID, "cancel", "cancelled") {
		t.Fatalf("expected cancel timeout audit event for job %s", job.ID)
	}
}

func TestProcessTimedOutApprovalJobs_ReadsOnlyAutoResumesReadLevelJobs(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.Jobs.ApprovalTimeoutBehavior = "reads_only"

	registerRemoteProbe(t, srv, "probe-timeout-reads")
	srv.remoteExecutor = &fakeRemoteExecutor{execResult: &protocol.CommandResultPayload{RequestID: "req-timeout-reads", ExitCode: 0, Stdout: "ok"}}

	job, err := srv.asyncJobsManager.CreateForCommand("probe-timeout-reads", protocol.CommandPayload{RequestID: "req-timeout-reads", Command: "ls", Args: []string{"-la"}, Level: protocol.CapObserve})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	approvalReq, err := srv.approvalQueue.Submit("probe-timeout-reads", &protocol.CommandPayload{RequestID: job.RequestID, Command: job.Command, Args: job.Args, Level: protocol.CapObserve}, "timeout test", "low", "tester")
	if err != nil {
		t.Fatalf("submit approval request: %v", err)
	}
	expiresAt := time.Now().UTC().Add(-time.Second)
	if _, err := srv.asyncJobsManager.MarkWaitingApproval(job.ID, approvalReq.ID, "waiting", &expiresAt); err != nil {
		t.Fatalf("mark waiting approval: %v", err)
	}

	processed, err := srv.processTimedOutApprovalJobs(time.Now().UTC())
	if err != nil {
		t.Fatalf("process timeout jobs: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 processed timeout job, got %d", processed)
	}

	updated := waitForAsyncJobState(t, srv, job.ID, jobs.AsyncJobStateRunning, jobs.AsyncJobStateSucceeded)
	if updated.ID != job.ID {
		t.Fatalf("expected same job id after reads_only resume, got %s want %s", updated.ID, job.ID)
	}

	events := srv.queryAudit(audit.Filter{Limit: 100})
	if !hasApprovalTimeoutAudit(events, job.ID, "reads_only", "auto_approved") {
		t.Fatalf("expected reads_only auto-approve audit event for job %s", job.ID)
	}
}

func TestProcessTimedOutApprovalJobs_EscalateBehavior(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.Jobs.ApprovalTimeoutBehavior = "escalate"
	srv.cfg.Jobs.ApprovalTimeoutSeconds = 60

	job, err := srv.asyncJobsManager.CreateForCommand("probe-timeout-escalate", protocol.CommandPayload{RequestID: "req-timeout-escalate", Command: "systemctl", Args: []string{"restart", "nginx"}, Level: protocol.CapRemediate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	expiresAt := time.Now().UTC().Add(-time.Second)
	if _, err := srv.asyncJobsManager.MarkWaitingApproval(job.ID, "apr-timeout-escalate", "waiting", &expiresAt); err != nil {
		t.Fatalf("mark waiting approval: %v", err)
	}

	processed, err := srv.processTimedOutApprovalJobs(time.Now().UTC())
	if err != nil {
		t.Fatalf("process timeout jobs: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 processed timeout job, got %d", processed)
	}

	updated := waitForAsyncJobState(t, srv, job.ID, jobs.AsyncJobStateWaitingApproval)
	if updated.ExpiresAt == nil || !updated.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("expected escalated job expiry to be extended")
	}

	events := srv.queryAudit(audit.Filter{Limit: 100})
	if !hasApprovalTimeoutAudit(events, job.ID, "escalate", "extended") {
		t.Fatalf("expected escalation timeout audit event for job %s", job.ID)
	}
}

func TestHandleApproveAsyncJob_ConcurrentRace(t *testing.T) {
	srv := newTestServer(t)
	job := createPendingApprovalAsyncJob(t, srv, "remote-race", "req-remote-race")

	makeReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/approve", bytes.NewBufferString(`{"decided_by":"operator"}`))
		req.SetPathValue("id", job.ID)
		return req
	}

	var wg sync.WaitGroup
	wg.Add(2)
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		idx := i
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			srv.handleApproveAsyncJob(rr, makeReq())
			codes[idx] = rr.Code
		}()
	}
	wg.Wait()

	okCount := 0
	conflictCount := 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("expected one success and one conflict, got codes=%v", codes)
	}
}
