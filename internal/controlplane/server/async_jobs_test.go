package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	registerRemoteProbe(t, srv, "remote-2")
	srv.remoteExecutor = &fakeRemoteExecutor{}

	payload := protocol.CommandPayload{RequestID: "req-remote-approval", Command: "systemctl", Args: []string{"restart", "nginx"}, Level: protocol.CapRemediate}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/remote-2/command", bytes.NewReader(body))
	req.SetPathValue("id", "remote-2")
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
}
