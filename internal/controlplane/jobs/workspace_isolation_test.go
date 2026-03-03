package jobs

import (
"fmt"
"testing"

"github.com/marcus-qen/legator/internal/protocol"
)

func setupTestAsyncStore(t *testing.T) *Store {
t.Helper()
return newTestStore(t)
}

var testCommandSeq int

func testCommand(cmd string) protocol.CommandPayload {
testCommandSeq++
return protocol.CommandPayload{
RequestID: fmt.Sprintf("req-%s-%d", cmd, testCommandSeq),
Command:   cmd,
}
}

func TestListAsyncJobsByWorkspace_isolation(t *testing.T) {
store := setupTestAsyncStore(t)
m := NewAsyncManager(store)

// Create jobs in two different workspaces.
jobA, err := m.CreateForCommandInWorkspace("probe-a", "workspace-a", testCommand("cmd-a"))
if err != nil {
t.Fatalf("create job a: %v", err)
}
_, err = m.CreateForCommandInWorkspace("probe-b", "workspace-b", testCommand("cmd-b"))
if err != nil {
t.Fatalf("create job b: %v", err)
}

// workspace-a should only see its own job.
got, err := m.ListJobsByWorkspace("workspace-a", 100)
if err != nil {
t.Fatalf("list workspace-a: %v", err)
}
if len(got) != 1 {
t.Fatalf("expected 1 job for workspace-a, got %d", len(got))
}
if got[0].ID != jobA.ID {
t.Fatalf("unexpected job ID: got %s want %s", got[0].ID, jobA.ID)
}
}

func TestGetAsyncJobInWorkspace_isolation(t *testing.T) {
store := setupTestAsyncStore(t)
m := NewAsyncManager(store)

jobA, err := m.CreateForCommandInWorkspace("probe-a", "workspace-a", testCommand("cmd-a"))
if err != nil {
t.Fatalf("create job a: %v", err)
}

// workspace-a can fetch its own job.
got, err := store.GetAsyncJobInWorkspace(jobA.ID, "workspace-a")
if err != nil {
t.Fatalf("get in workspace-a: %v", err)
}
if got.ID != jobA.ID {
t.Fatalf("unexpected job ID: got %s want %s", got.ID, jobA.ID)
}

// workspace-b cannot see workspace-a's job.
_, err = store.GetAsyncJobInWorkspace(jobA.ID, "workspace-b")
if err == nil {
t.Fatal("expected error for cross-workspace get, got nil")
}
}

func TestWorkspaceIDPreservedOnCreate(t *testing.T) {
store := setupTestAsyncStore(t)
m := NewAsyncManager(store)

job, err := m.CreateForCommandInWorkspace("probe", "my-workspace", testCommand("echo"))
if err != nil {
t.Fatalf("create: %v", err)
}
if job.WorkspaceID != "my-workspace" {
t.Fatalf("workspace not stored: got %q want %q", job.WorkspaceID, "my-workspace")
}

fetched, err := store.GetAsyncJob(job.ID)
if err != nil {
t.Fatalf("get: %v", err)
}
if fetched.WorkspaceID != "my-workspace" {
t.Fatalf("workspace not persisted: got %q want %q", fetched.WorkspaceID, "my-workspace")
}
}
