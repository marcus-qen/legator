package sandbox

import (
	"testing"
	"time"
)

// newTestTaskStore creates a Store + TaskStore sharing the same SQLite db.
func newTestTaskStore(t *testing.T) (*Store, *TaskStore) {
	t.Helper()
	store := newTestStore(t)
	ts, err := NewTaskStore(store.DB())
	if err != nil {
		t.Fatalf("NewTaskStore: %v", err)
	}
	return store, ts
}

// makeTask returns a minimal Task for use in tests.
func makeTask(sandboxID, workspaceID, kind string) *Task {
	task := &Task{
		SandboxID:   sandboxID,
		WorkspaceID: workspaceID,
		Kind:        kind,
		TimeoutSecs: 60,
	}
	switch kind {
	case TaskKindCommand:
		task.Command = []string{"echo", "hello"}
	case TaskKindRepo:
		task.RepoURL = "https://github.com/example/repo"
		task.RepoBranch = "main"
		task.RepoCommand = []string{"make", "test"}
	}
	return task
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

func TestTaskStore_CreateAndGet(t *testing.T) {
	_, ts := newTestTaskStore(t)

	task, err := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected generated ID")
	}
	if task.State != TaskStateQueued {
		t.Fatalf("expected state %q, got %q", TaskStateQueued, task.State)
	}
	if task.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}

	got, err := ts.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetTask returned nil")
	}
	if got.SandboxID != "sbx-1" {
		t.Fatalf("wrong sandbox_id: %q", got.SandboxID)
	}
	if got.Kind != TaskKindCommand {
		t.Fatalf("wrong kind: %q", got.Kind)
	}
	if len(got.Command) != 2 || got.Command[0] != "echo" {
		t.Fatalf("command not preserved: %v", got.Command)
	}
}

func TestTaskStore_GetNotFound(t *testing.T) {
	_, ts := newTestTaskStore(t)
	got, err := ts.GetTask("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestTaskStore_CreateRepo(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task, err := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindRepo))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, _ := ts.GetTask(task.ID)
	if got.RepoURL != "https://github.com/example/repo" {
		t.Fatalf("repo_url not preserved: %q", got.RepoURL)
	}
	if got.RepoBranch != "main" {
		t.Fatalf("repo_branch not preserved: %q", got.RepoBranch)
	}
	if len(got.RepoCommand) != 2 || got.RepoCommand[0] != "make" {
		t.Fatalf("repo_command not preserved: %v", got.RepoCommand)
	}
}

func TestTaskStore_DefaultTimeout(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task := makeTask("sbx-1", "ws-1", TaskKindCommand)
	task.TimeoutSecs = 0 // zero → should use default
	created, err := ts.CreateTask(task)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.TimeoutSecs != DefaultTaskTimeoutSecs {
		t.Fatalf("expected default timeout %d, got %d", DefaultTaskTimeoutSecs, created.TimeoutSecs)
	}
}

func TestTaskStore_MaxTimeoutCapped(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task := makeTask("sbx-1", "ws-1", TaskKindCommand)
	task.TimeoutSecs = 99999 // over max
	created, err := ts.CreateTask(task)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.TimeoutSecs != MaxTaskTimeoutSecs {
		t.Fatalf("expected max timeout %d, got %d", MaxTaskTimeoutSecs, created.TimeoutSecs)
	}
}

func TestTaskStore_OutputCapped(t *testing.T) {
	_, ts := newTestTaskStore(t)
	bigOutput := make([]byte, MaxOutputBytes+1000)
	for i := range bigOutput {
		bigOutput[i] = 'x'
	}
	task := makeTask("sbx-1", "ws-1", TaskKindCommand)
	task.Output = string(bigOutput)
	created, err := ts.CreateTask(task)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, _ := ts.GetTask(created.ID)
	if len(got.Output) > MaxOutputBytes {
		t.Fatalf("output should be capped at %d bytes, got %d", MaxOutputBytes, len(got.Output))
	}
}

// ── List ─────────────────────────────────────────────────────────────────────

func TestTaskStore_List(t *testing.T) {
	_, ts := newTestTaskStore(t)

	for i := 0; i < 3; i++ {
		ts.CreateTask(makeTask("sbx-a", "ws-1", TaskKindCommand))
	}
	for i := 0; i < 2; i++ {
		ts.CreateTask(makeTask("sbx-b", "ws-1", TaskKindCommand))
	}

	all, err := ts.ListTasks(TaskListFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 tasks, got %d", len(all))
	}

	sbxA, err := ts.ListTasks(TaskListFilter{SandboxID: "sbx-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sbxA) != 3 {
		t.Fatalf("expected 3 tasks for sbx-a, got %d", len(sbxA))
	}

	sbxB, _ := ts.ListTasks(TaskListFilter{SandboxID: "sbx-b"})
	if len(sbxB) != 2 {
		t.Fatalf("expected 2 tasks for sbx-b, got %d", len(sbxB))
	}
}

func TestTaskStore_ListByWorkspace(t *testing.T) {
	_, ts := newTestTaskStore(t)

	ts.CreateTask(makeTask("sbx-1", "ws-a", TaskKindCommand))
	ts.CreateTask(makeTask("sbx-1", "ws-b", TaskKindCommand))

	wsA, _ := ts.ListTasks(TaskListFilter{WorkspaceID: "ws-a"})
	if len(wsA) != 1 {
		t.Fatalf("expected 1 task for ws-a, got %d", len(wsA))
	}
}

func TestTaskStore_ListByState(t *testing.T) {
	_, ts := newTestTaskStore(t)

	t1, _ := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))
	ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))

	ts.TransitionTask(t1.ID, TaskStateQueued, TaskStateRunning)

	queued, _ := ts.ListTasks(TaskListFilter{State: TaskStateQueued})
	if len(queued) != 1 {
		t.Fatalf("expected 1 queued task, got %d", len(queued))
	}
	running, _ := ts.ListTasks(TaskListFilter{State: TaskStateRunning})
	if len(running) != 1 {
		t.Fatalf("expected 1 running task, got %d", len(running))
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestTaskStore_Update(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task, _ := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))

	now := time.Now().UTC()
	task.ExitCode = 42
	task.Output = "hello world"
	task.ErrorMessage = "some error"
	task.StartedAt = &now

	if err := ts.UpdateTask(task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	got, _ := ts.GetTask(task.ID)
	if got.ExitCode != 42 {
		t.Fatalf("ExitCode not updated: %d", got.ExitCode)
	}
	if got.Output != "hello world" {
		t.Fatalf("Output not updated: %q", got.Output)
	}
	if got.StartedAt == nil {
		t.Fatal("StartedAt not updated")
	}
}

func TestTaskStore_UpdateNotFound(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task := &Task{ID: "ghost", ExitCode: 1}
	if err := ts.UpdateTask(task); err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

// ── Transitions ───────────────────────────────────────────────────────────────

func TestTaskStore_ValidTransitions(t *testing.T) {
	valid := []struct{ from, to string }{
		{TaskStateQueued, TaskStateRunning},
		{TaskStateQueued, TaskStateCancelled},
		{TaskStateRunning, TaskStateSucceeded},
		{TaskStateRunning, TaskStateFailed},
		{TaskStateRunning, TaskStateCancelled},
	}
	for _, tc := range valid {
		if err := ValidateTaskTransition(tc.from, tc.to); err != nil {
			t.Errorf("expected valid %q→%q but got: %v", tc.from, tc.to, err)
		}
	}
}

func TestTaskStore_InvalidTransitions(t *testing.T) {
	invalid := []struct{ from, to string }{
		{TaskStateQueued, TaskStateSucceeded},
		{TaskStateQueued, TaskStateFailed},
		{TaskStateRunning, TaskStateQueued},
		{TaskStateSucceeded, TaskStateQueued},
		{TaskStateFailed, TaskStateRunning},
		{TaskStateCancelled, TaskStateQueued},
	}
	for _, tc := range invalid {
		if err := ValidateTaskTransition(tc.from, tc.to); err == nil {
			t.Errorf("expected invalid %q→%q but no error", tc.from, tc.to)
		}
	}
}

func TestTaskStore_Transition_QueuedToRunning(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task, _ := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))

	updated, err := ts.TransitionTask(task.ID, TaskStateQueued, TaskStateRunning)
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if updated.State != TaskStateRunning {
		t.Fatalf("expected running, got %q", updated.State)
	}
	if updated.StartedAt == nil {
		t.Fatal("expected StartedAt to be set on running transition")
	}
}

func TestTaskStore_Transition_FullLifecycle(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task, _ := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))

	steps := []struct{ from, to string }{
		{TaskStateQueued, TaskStateRunning},
		{TaskStateRunning, TaskStateSucceeded},
	}
	for _, step := range steps {
		updated, err := ts.TransitionTask(task.ID, step.from, step.to)
		if err != nil {
			t.Fatalf("TransitionTask %q→%q: %v", step.from, step.to, err)
		}
		if updated.State != step.to {
			t.Fatalf("expected %q, got %q", step.to, updated.State)
		}
	}

	final, _ := ts.GetTask(task.ID)
	if final.State != TaskStateSucceeded {
		t.Fatalf("expected succeeded, got %q", final.State)
	}
	if final.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set on terminal transition")
	}
}

func TestTaskStore_Transition_CancelQueued(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task, _ := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))

	updated, err := ts.TransitionTask(task.ID, TaskStateQueued, TaskStateCancelled)
	if err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if updated.State != TaskStateCancelled {
		t.Fatalf("expected cancelled, got %q", updated.State)
	}
	if updated.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set")
	}
}

func TestTaskStore_Transition_StateMismatch(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task, _ := ts.CreateTask(makeTask("sbx-1", "ws-1", TaskKindCommand))

	// Provide wrong fromState.
	if _, err := ts.TransitionTask(task.ID, TaskStateRunning, TaskStateSucceeded); err == nil {
		t.Fatal("expected error for state mismatch")
	}

	// Task should still be in queued.
	got, _ := ts.GetTask(task.ID)
	if got.State != TaskStateQueued {
		t.Fatalf("state should not have changed: %q", got.State)
	}
}

func TestTaskStore_Transition_NonExistent(t *testing.T) {
	_, ts := newTestTaskStore(t)
	if _, err := ts.TransitionTask("ghost-id", TaskStateQueued, TaskStateRunning); err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

// ── Workspace isolation ───────────────────────────────────────────────────────

func TestTaskStore_WorkspaceIsolation(t *testing.T) {
	_, ts := newTestTaskStore(t)
	task, _ := ts.CreateTask(makeTask("sbx-1", "workspace-A", TaskKindCommand))

	// Workspace B cannot see workspace A's task.
	got, err := ts.GetTaskForWorkspace(task.ID, "workspace-B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatal("workspace isolation breach: workspace-B can read workspace-A task")
	}

	// Workspace A can see it.
	got, err = ts.GetTaskForWorkspace(task.ID, "workspace-A")
	if err != nil || got == nil {
		t.Fatalf("workspace-A should see its own task: err=%v, got=%v", err, got)
	}

	// Empty workspace skips check.
	got, err = ts.GetTaskForWorkspace(task.ID, "")
	if err != nil || got == nil {
		t.Fatalf("empty workspace should not filter: err=%v", err)
	}
}
