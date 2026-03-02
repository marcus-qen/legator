package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func newTestServerWithJobsConfig(t *testing.T, jobsCfg config.JobsConfig) *Server {
	t.Helper()
	t.Setenv("LEGATOR_LLM_PROVIDER", "")
	t.Setenv("LEGATOR_AUTH", "0")
	t.Setenv("LEGATOR_SIGNING_KEY", strings.Repeat("a", 64))

	cfg := config.Config{
		ListenAddr: ":0",
		DataDir:    t.TempDir(),
		Jobs:       jobsCfg,
	}
	srv, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestHandleDispatchCommand_QueuesWhenSchedulerAtGlobalLimit(t *testing.T) {
	srv := newTestServerWithJobsConfig(t, config.JobsConfig{AsyncMaxInFlight: 1, AsyncMaxQueueDepth: 20})
	srv.fleetMgr.Register("probe-saturated", "host", "linux", "amd64")

	running, err := srv.asyncJobsManager.CreateJob(jobs.AsyncJob{ProbeID: "probe-saturated", RequestID: "req-running", Command: "running"})
	if err != nil {
		t.Fatalf("create running seed job: %v", err)
	}
	if _, err := srv.jobsStore.TransitionAsyncJob(running.ID, jobs.AsyncJobStateRunning, jobs.AsyncJobTransitionOptions{}); err != nil {
		t.Fatalf("seed running state: %v", err)
	}

	payload := protocol.CommandPayload{RequestID: "req-new", Command: "ls", Level: protocol.CapObserve}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-saturated/command", bytes.NewReader(body))
	req.SetPathValue("id", "probe-saturated")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202 queued, got %d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode queue response: %v", err)
	}
	if got["status"] != "queued" {
		t.Fatalf("expected status queued, got %v", got["status"])
	}
	if got["reason"] != "global_limit" {
		t.Fatalf("expected reason global_limit, got %v", got["reason"])
	}
	jobID, _ := got["job_id"].(string)
	if jobID == "" {
		t.Fatal("expected queued job_id in response")
	}
	queued, err := srv.asyncJobsManager.GetJob(jobID)
	if err != nil {
		t.Fatalf("get queued async job: %v", err)
	}
	if queued.State != jobs.AsyncJobStateQueued {
		t.Fatalf("expected queued state, got %s", queued.State)
	}
}

func TestHandleDispatchCommand_RejectsWhenAsyncQueueSaturated(t *testing.T) {
	srv := newTestServerWithJobsConfig(t, config.JobsConfig{AsyncMaxInFlight: 1, AsyncMaxQueueDepth: 1})
	srv.fleetMgr.Register("probe-queue-full", "host", "linux", "amd64")

	if _, err := srv.asyncJobsManager.CreateJob(jobs.AsyncJob{ProbeID: "probe-queue-full", RequestID: "req-existing", Command: "echo existing"}); err != nil {
		t.Fatalf("seed queued job: %v", err)
	}

	payload := protocol.CommandPayload{RequestID: "req-saturated", Command: "ls", Level: protocol.CapObserve}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-queue-full/command", bytes.NewReader(body))
	req.SetPathValue("id", "probe-queue-full")
	rr := httptest.NewRecorder()

	srv.handleDispatchCommand(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "queue saturated") {
		t.Fatalf("expected queue saturated message, got %s", rr.Body.String())
	}
}
