package jobs

import (
	"testing"
)

func TestStoreListJobsByWorkspace(t *testing.T) {
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Create jobs in workspace A and B
	jobA, err := s.CreateJob(Job{
		WorkspaceID: "ws-a",
		Name:        "job-in-a",
		Command:     "echo a",
		Schedule:    "@hourly",
		Target:      Target{Kind: TargetKindAll},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.CreateJob(Job{
		WorkspaceID: "ws-b",
		Name:        "job-in-b",
		Command:     "echo b",
		Schedule:    "@hourly",
		Target:      Target{Kind: TargetKindAll},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// List ws-a only
	jobs, err := s.ListJobsByWorkspace("ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job in ws-a, got %d", len(jobs))
	}
	if jobs[0].Name != "job-in-a" {
		t.Errorf("expected job-in-a, got %s", jobs[0].Name)
	}

	// List ws-b only
	jobs, err = s.ListJobsByWorkspace("ws-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Name != "job-in-b" {
		t.Fatalf("expected 1 job-in-b, got %+v", jobs)
	}

	// List all (empty filter)
	jobs, err = s.ListJobsByWorkspace("")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs total, got %d", len(jobs))
	}

	// GetJobCheckWorkspace — happy path
	got, err := s.GetJobCheckWorkspace(jobA.ID, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "ws-a" {
		t.Errorf("expected ws-a, got %s", got.WorkspaceID)
	}

	// GetJobCheckWorkspace — mismatch returns ErrWorkspaceMismatch
	_, err = s.GetJobCheckWorkspace(jobA.ID, "ws-b")
	if err != ErrWorkspaceMismatch {
		t.Fatalf("expected ErrWorkspaceMismatch, got %v", err)
	}

	// GetJobCheckWorkspace — empty expected workspace bypasses check
	got, err = s.GetJobCheckWorkspace(jobA.ID, "")
	if err != nil {
		t.Fatalf("empty workspace check should pass, got %v", err)
	}
	if got.ID != jobA.ID {
		t.Errorf("unexpected job id: %s", got.ID)
	}
}

func TestStoreListRunsByWorkspace(t *testing.T) {
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	jobA, err := s.CreateJob(Job{
		WorkspaceID: "ws-a",
		Name:        "j-a",
		Command:     "echo a",
		Schedule:    "@hourly",
		Target:      Target{Kind: TargetKindAll},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobB, err := s.CreateJob(Job{
		WorkspaceID: "ws-b",
		Name:        "j-b",
		Command:     "echo b",
		Schedule:    "@hourly",
		Target:      Target{Kind: TargetKindAll},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	runA, err := s.RecordRunStart(JobRun{JobID: jobA.ID, ProbeID: "p-a", RequestID: "req-a", Status: RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	runB, err := s.RecordRunStart(JobRun{JobID: jobB.ID, ProbeID: "p-b", RequestID: "req-b", Status: RunStatusRunning})
	if err != nil {
		t.Fatal(err)
	}

	if runA.WorkspaceID != "ws-a" {
		t.Fatalf("expected runA workspace ws-a, got %q", runA.WorkspaceID)
	}
	if runB.WorkspaceID != "ws-b" {
		t.Fatalf("expected runB workspace ws-b, got %q", runB.WorkspaceID)
	}

	runsA, err := s.ListRuns(RunQuery{WorkspaceID: "ws-a", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(runsA) != 1 || runsA[0].RequestID != "req-a" {
		t.Fatalf("expected only ws-a run, got %+v", runsA)
	}

	runsB, err := s.ListRuns(RunQuery{WorkspaceID: "ws-b", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(runsB) != 1 || runsB[0].RequestID != "req-b" {
		t.Fatalf("expected only ws-b run, got %+v", runsB)
	}

	allRuns, err := s.ListRuns(RunQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(allRuns) != 2 {
		t.Fatalf("expected 2 runs without workspace filter, got %d", len(allRuns))
	}
}


func TestAsyncStoreWorkspaceIsolation(t *testing.T) {
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	m := NewAsyncManager(s)

	// Create job in ws-a
	jobA, err := m.CreateJob(AsyncJob{
		WorkspaceID: "ws-a",
		ProbeID:     "probe-1",
		RequestID:   "req-ws-a-1",
		Command:     "ls",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create job in ws-b
	jobB, err := m.CreateJob(AsyncJob{
		WorkspaceID: "ws-b",
		ProbeID:     "probe-2",
		RequestID:   "req-ws-b-1",
		Command:     "date",
	})
	if err != nil {
		t.Fatal(err)
	}

	// ListAsyncJobsByWorkspace filters correctly
	wsAJobs, err := s.ListAsyncJobsByWorkspace("ws-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wsAJobs) != 1 || wsAJobs[0].ID != jobA.ID {
		t.Fatalf("expected 1 ws-a job, got %+v", wsAJobs)
	}

	wsBJobs, err := s.ListAsyncJobsByWorkspace("ws-b", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wsBJobs) != 1 || wsBJobs[0].ID != jobB.ID {
		t.Fatalf("expected 1 ws-b job, got %+v", wsBJobs)
	}

	// Empty workspace → all jobs
	all, err := s.ListAsyncJobsByWorkspace("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 total jobs, got %d", len(all))
	}

	// GetAsyncJobCheckWorkspace
	got, err := s.GetAsyncJobCheckWorkspace(jobA.ID, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "ws-a" {
		t.Errorf("expected ws-a, got %s", got.WorkspaceID)
	}

	// Cross-workspace access denied
	_, err = s.GetAsyncJobCheckWorkspace(jobA.ID, "ws-b")
	if err != ErrWorkspaceMismatch {
		t.Fatalf("expected ErrWorkspaceMismatch cross-workspace, got %v", err)
	}

	// Empty expected workspace → passes
	got, err = s.GetAsyncJobCheckWorkspace(jobB.ID, "")
	if err != nil {
		t.Fatalf("empty workspace check should pass, got %v", err)
	}
	if got.ID != jobB.ID {
		t.Errorf("unexpected job id: %s", got.ID)
	}
}
