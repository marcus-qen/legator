package server

import (
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"go.uber.org/zap"
)

func TestFailureDrill_ControlPlaneRestartRecoversQueuedAndRunningJobs(t *testing.T) {
	dataDir := t.TempDir()
	jobsCfg := config.JobsConfig{
		AsyncMaxInFlight:   2,
		AsyncMaxQueueDepth: 64,
	}

	srv := newFailureDrillServer(t, dataDir, jobsCfg)
	queued, err := srv.asyncJobsManager.CreateJob(jobs.AsyncJob{ProbeID: "probe-drill", RequestID: "req-drill-queued", Command: "echo queued"})
	if err != nil {
		t.Fatalf("create queued job: %v", err)
	}

	running, err := srv.asyncJobsManager.CreateJob(jobs.AsyncJob{ProbeID: "probe-drill", RequestID: "req-drill-running", Command: "echo running"})
	if err != nil {
		t.Fatalf("create running job: %v", err)
	}
	if _, err := srv.jobsStore.TransitionAsyncJob(running.ID, jobs.AsyncJobStateRunning, jobs.AsyncJobTransitionOptions{}); err != nil {
		t.Fatalf("seed running state: %v", err)
	}
	srv.Close()

	restarted := newFailureDrillServer(t, dataDir, jobsCfg)
	t.Cleanup(func() { restarted.Close() })

	queuedAfter, err := restarted.asyncJobsManager.GetJob(queued.ID)
	if err != nil {
		t.Fatalf("get queued job after restart: %v", err)
	}
	if queuedAfter.State != jobs.AsyncJobStateQueued {
		t.Fatalf("expected queued job to remain queued after restart, got %s", queuedAfter.State)
	}

	runningAfter, err := restarted.asyncJobsManager.GetJob(running.ID)
	if err != nil {
		t.Fatalf("get running job after restart: %v", err)
	}
	if runningAfter.State != jobs.AsyncJobStateExpired {
		t.Fatalf("expected running job to expire on restart, got %s", runningAfter.State)
	}
	if !strings.Contains(runningAfter.StatusReason, "control plane restarted") {
		t.Fatalf("expected restart reason, got %q", runningAfter.StatusReason)
	}
}

func newFailureDrillServer(t *testing.T, dataDir string, jobsCfg config.JobsConfig) *Server {
	t.Helper()
	t.Setenv("LEGATOR_LLM_PROVIDER", "")
	t.Setenv("LEGATOR_AUTH", "0")
	t.Setenv("LEGATOR_SIGNING_KEY", strings.Repeat("a", 64))

	srv, err := New(config.Config{
		ListenAddr: ":0",
		DataDir:    dataDir,
		Jobs:       jobsCfg,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}
