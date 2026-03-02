package runner

import (
	"errors"
	"testing"
	"time"
)

func TestRunTokenReuseRejected(t *testing.T) {
	now := time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)
	mgr := NewManager(Config{RunTokenTTL: 2 * time.Minute, Now: func() time.Time { return now }})

	r, err := mgr.CreateRunner(CreateRequest{SessionID: "sess-1", CreatedBy: "alice"})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	issued, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStart, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	if _, err := mgr.StartRunner(LifecycleRequest{RunnerID: r.ID, RunToken: issued.Token, SessionID: "sess-1"}); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	if _, err := mgr.StartRunner(LifecycleRequest{RunnerID: r.ID, RunToken: issued.Token, SessionID: "sess-1"}); !errors.Is(err, ErrRunTokenConsumed) {
		t.Fatalf("expected token consumed error, got %v", err)
	}
}

func TestRunTokenExpiryRejected(t *testing.T) {
	now := time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)
	mgr := NewManager(Config{RunTokenTTL: 2 * time.Second, Now: func() time.Time { return now }})

	r, err := mgr.CreateRunner(CreateRequest{SessionID: "sess-1", CreatedBy: "alice"})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	if _, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStart, SessionID: "sess-1", TTL: time.Second}); err != nil {
		t.Fatalf("issue start token: %v", err)
	}
	startToken, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStart, SessionID: "sess-1", TTL: time.Second})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	now = now.Add(2 * time.Second)
	if _, err := mgr.StartRunner(LifecycleRequest{RunnerID: r.ID, RunToken: startToken.Token, SessionID: "sess-1"}); !errors.Is(err, ErrRunTokenExpired) {
		t.Fatalf("expected expired token error, got %v", err)
	}
}

func TestRunTokenScopeRejection(t *testing.T) {
	now := time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)
	mgr := NewManager(Config{RunTokenTTL: 2 * time.Minute, Now: func() time.Time { return now }})

	r1, err := mgr.CreateRunner(CreateRequest{SessionID: "sess-1", CreatedBy: "alice"})
	if err != nil {
		t.Fatalf("create runner 1: %v", err)
	}
	r2, err := mgr.CreateRunner(CreateRequest{SessionID: "sess-1", CreatedBy: "alice"})
	if err != nil {
		t.Fatalf("create runner 2: %v", err)
	}

	startToken, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r1.ID, Audience: AudienceRunnerStart, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	if _, err := mgr.StopRunner(LifecycleRequest{RunnerID: r1.ID, RunToken: startToken.Token, SessionID: "sess-1"}); !errors.Is(err, ErrRunTokenScope) {
		t.Fatalf("expected scope rejection on audience mismatch, got %v", err)
	}

	startToken2, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r1.ID, Audience: AudienceRunnerStart, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue token 2: %v", err)
	}
	if _, err := mgr.StartRunner(LifecycleRequest{RunnerID: r2.ID, RunToken: startToken2.Token, SessionID: "sess-1"}); !errors.Is(err, ErrRunTokenScope) {
		t.Fatalf("expected scope rejection on runner mismatch, got %v", err)
	}

	startToken3, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r1.ID, Audience: AudienceRunnerStart, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue token 3: %v", err)
	}
	if _, err := mgr.StartRunner(LifecycleRequest{RunnerID: r1.ID, RunToken: startToken3.Token, SessionID: "sess-2"}); !errors.Is(err, ErrRunTokenSessionBound) {
		t.Fatalf("expected session binding rejection, got %v", err)
	}
}

func TestRunnerLifecycleTransitions(t *testing.T) {
	now := time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)
	mgr := NewManager(Config{RunTokenTTL: 5 * time.Minute, Now: func() time.Time { return now }})

	r, err := mgr.CreateRunner(CreateRequest{SessionID: "sess-1", CreatedBy: "alice"})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	if r.State != StateCreated {
		t.Fatalf("expected created state, got %s", r.State)
	}

	start1, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStart, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue start token: %v", err)
	}
	r, err = mgr.StartRunner(LifecycleRequest{RunnerID: r.ID, RunToken: start1.Token, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("start runner: %v", err)
	}
	if r.State != StateRunning {
		t.Fatalf("expected running state, got %s", r.State)
	}

	stop, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStop, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue stop token: %v", err)
	}
	r, err = mgr.StopRunner(LifecycleRequest{RunnerID: r.ID, RunToken: stop.Token, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("stop runner: %v", err)
	}
	if r.State != StateStopped {
		t.Fatalf("expected stopped state, got %s", r.State)
	}

	start2, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStart, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue restart token: %v", err)
	}
	r, err = mgr.StartRunner(LifecycleRequest{RunnerID: r.ID, RunToken: start2.Token, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("restart runner: %v", err)
	}
	if r.State != StateRunning {
		t.Fatalf("expected running after restart, got %s", r.State)
	}

	destroy, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerDestroy, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue destroy token: %v", err)
	}
	r, err = mgr.DestroyRunner(LifecycleRequest{RunnerID: r.ID, RunToken: destroy.Token, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("destroy runner: %v", err)
	}
	if r.State != StateDestroyed {
		t.Fatalf("expected destroyed state, got %s", r.State)
	}

	stopAfterDestroy, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStop, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("issue stop-after-destroy token: %v", err)
	}
	if _, err := mgr.StopRunner(LifecycleRequest{RunnerID: r.ID, RunToken: stopAfterDestroy.Token, SessionID: "sess-1"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition from destroyed -> stopped, got %v", err)
	}
}

func TestSandboxRunnerRequiresCommandContract(t *testing.T) {
	mgr := NewManager(Config{})
	if _, err := mgr.CreateRunner(CreateRequest{SessionID: "sess-1", Backend: BackendSandbox}); !errors.Is(err, ErrSandboxCommandRequired) {
		t.Fatalf("expected sandbox command required, got %v", err)
	}

	r, err := mgr.CreateRunner(CreateRequest{
		SessionID: "sess-1",
		Backend:   BackendSandbox,
		Sandbox: &SandboxContract{
			Image:          "alpine:3.20",
			Command:        []string{"sh", "-lc", "echo hi"},
			TimeoutSeconds: 10,
		},
		JobID: "job-1",
	})
	if err != nil {
		t.Fatalf("create sandbox runner: %v", err)
	}
	if r.Backend != BackendSandbox {
		t.Fatalf("expected sandbox backend, got %s", r.Backend)
	}
	if r.JobID != "job-1" {
		t.Fatalf("expected job_id job-1, got %s", r.JobID)
	}
	if r.Sandbox == nil || len(r.Sandbox.Command) == 0 {
		t.Fatalf("expected sandbox contract command")
	}
}

func TestRunTokenJobBindingScope(t *testing.T) {
	now := time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)
	mgr := NewManager(Config{RunTokenTTL: 2 * time.Minute, Now: func() time.Time { return now }})

	r, err := mgr.CreateRunner(CreateRequest{SessionID: "sess-1", JobID: "job-1"})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}

	issued, err := mgr.IssueRunToken(IssueTokenRequest{RunnerID: r.ID, Audience: AudienceRunnerStart, SessionID: "sess-1", JobID: "job-1"})
	if err != nil {
		t.Fatalf("issue run token: %v", err)
	}

	if _, err := mgr.StartRunner(LifecycleRequest{RunnerID: r.ID, JobID: "job-2", RunToken: issued.Token, SessionID: "sess-1"}); !errors.Is(err, ErrRunTokenScope) {
		t.Fatalf("expected run token scope rejection on mismatched job id, got %v", err)
	}
}
