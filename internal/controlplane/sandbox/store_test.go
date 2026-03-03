package sandbox

import (
	"path/filepath"
	"testing"
	"time"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sandbox.db")
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(tempDB(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func makeSession(workspaceID, probeID string) *SandboxSession {
	return &SandboxSession{
		WorkspaceID:  workspaceID,
		ProbeID:      probeID,
		RuntimeClass: "kata",
		CreatedBy:    "test-user",
		Metadata:     map[string]string{"env": "test"},
	}
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

func TestStore_CreateAndGet(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.Create(makeSession("ws-1", "probe-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("expected generated ID")
	}
	if sess.State != StateCreated {
		t.Fatalf("expected state %q, got %q", StateCreated, sess.State)
	}
	if sess.CreatedAt.IsZero() || sess.UpdatedAt.IsZero() {
		t.Fatal("timestamps not set")
	}

	got, err := s.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.RuntimeClass != "kata" {
		t.Fatalf("wrong runtime class: %q", got.RuntimeClass)
	}
	if got.Metadata["env"] != "test" {
		t.Fatalf("metadata not preserved: %v", got.Metadata)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Get("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestStore_List(t *testing.T) {
	s := newTestStore(t)

	// Create sessions across two workspaces.
	for i := 0; i < 3; i++ {
		if _, err := s.Create(makeSession("ws-a", "probe-1")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Create(makeSession("ws-b", "probe-2")); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.List(ListFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 sessions, got %d", len(all))
	}

	wsA, err := s.List(ListFilter{WorkspaceID: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(wsA) != 3 {
		t.Fatalf("expected 3 sessions in ws-a, got %d", len(wsA))
	}

	wsB, err := s.List(ListFilter{WorkspaceID: "ws-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(wsB) != 2 {
		t.Fatalf("expected 2 sessions in ws-b, got %d", len(wsB))
	}
}

func TestStore_ListByState(t *testing.T) {
	s := newTestStore(t)

	sess1, _ := s.Create(makeSession("ws-1", "p1"))
	sess2, _ := s.Create(makeSession("ws-1", "p2"))

	// Transition sess1 to provisioning.
	if _, err := s.Transition(sess1.ID, StateCreated, StateProvisioning); err != nil {
		t.Fatal(err)
	}

	created, _ := s.List(ListFilter{State: StateCreated})
	if len(created) != 1 || created[0].ID != sess2.ID {
		t.Fatalf("expected only sess2 in created state, got %d sessions", len(created))
	}

	provisioning, _ := s.List(ListFilter{State: StateProvisioning})
	if len(provisioning) != 1 || provisioning[0].ID != sess1.ID {
		t.Fatalf("expected only sess1 in provisioning state, got %d sessions", len(provisioning))
	}
}

func TestStore_ListByProbe(t *testing.T) {
	s := newTestStore(t)

	s.Create(makeSession("ws-1", "alpha"))
	s.Create(makeSession("ws-1", "alpha"))
	s.Create(makeSession("ws-1", "beta"))

	alpha, _ := s.List(ListFilter{ProbeID: "alpha"})
	if len(alpha) != 2 {
		t.Fatalf("expected 2 for probe alpha, got %d", len(alpha))
	}
}

func TestStore_Update(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("ws-1", "p1"))

	sess.TaskID = "task-xyz"
	sess.Metadata["key"] = "value"
	if err := s.Update(sess); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := s.Get(sess.ID)
	if got.TaskID != "task-xyz" {
		t.Fatalf("TaskID not updated: %q", got.TaskID)
	}
	if got.Metadata["key"] != "value" {
		t.Fatalf("Metadata not updated: %v", got.Metadata)
	}
}

func TestStore_Delete(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("ws-1", "p1"))

	if err := s.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.Get(sess.ID)
	if err != nil || got != nil {
		t.Fatalf("expected nil after delete, got %v / err %v", got, err)
	}

	// Deleting again is an error.
	if err := s.Delete(sess.ID); err == nil {
		t.Fatal("expected error deleting nonexistent session")
	}
}

func TestStore_Count(t *testing.T) {
	s := newTestStore(t)
	s.Create(makeSession("ws-a", "p1"))
	s.Create(makeSession("ws-a", "p2"))
	s.Create(makeSession("ws-b", "p3"))

	if s.Count("") != 3 {
		t.Fatalf("expected 3 total, got %d", s.Count(""))
	}
	if s.Count("ws-a") != 2 {
		t.Fatalf("expected 2 for ws-a, got %d", s.Count("ws-a"))
	}
}

// ── State machine ────────────────────────────────────────────────────────────

func TestValidTransitions(t *testing.T) {
	valid := []struct{ from, to string }{
		{StateCreated, StateProvisioning},
		{StateProvisioning, StateReady},
		{StateProvisioning, StateFailed},
		{StateReady, StateRunning},
		{StateReady, StateDestroyed},
		{StateRunning, StateFailed},
		{StateRunning, StateDestroyed},
	}
	for _, tc := range valid {
		if err := ValidateTransition(tc.from, tc.to); err != nil {
			t.Errorf("expected valid %q→%q but got: %v", tc.from, tc.to, err)
		}
	}
}

func TestInvalidTransitions(t *testing.T) {
	invalid := []struct{ from, to string }{
		{StateCreated, StateReady},
		{StateCreated, StateRunning},
		{StateCreated, StateFailed},
		{StateCreated, StateDestroyed},
		{StateProvisioning, StateCreated},
		{StateProvisioning, StateRunning},
		{StateReady, StateProvisioning},
		{StateReady, StateCreated},
		{StateRunning, StateCreated},
		{StateRunning, StateProvisioning},
		{StateRunning, StateReady},
		{StateFailed, StateCreated},
		{StateFailed, StateDestroyed},
		{StateDestroyed, StateCreated},
		{StateDestroyed, StateFailed},
	}
	for _, tc := range invalid {
		if err := ValidateTransition(tc.from, tc.to); err == nil {
			t.Errorf("expected invalid %q→%q but no error", tc.from, tc.to)
		}
	}
}

func TestStore_Transition_Valid(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("ws-1", "p1"))

	updated, err := s.Transition(sess.ID, StateCreated, StateProvisioning)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if updated.State != StateProvisioning {
		t.Fatalf("expected provisioning, got %q", updated.State)
	}
}

func TestStore_Transition_FullLifecycle(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("ws-1", "p1"))

	steps := []struct{ from, to string }{
		{StateCreated, StateProvisioning},
		{StateProvisioning, StateReady},
		{StateReady, StateRunning},
		{StateRunning, StateDestroyed},
	}
	for _, step := range steps {
		updated, err := s.Transition(sess.ID, step.from, step.to)
		if err != nil {
			t.Fatalf("Transition %q→%q: %v", step.from, step.to, err)
		}
		if updated.State != step.to {
			t.Fatalf("expected %q, got %q", step.to, updated.State)
		}
	}

	final, _ := s.Get(sess.ID)
	if final.State != StateDestroyed {
		t.Fatalf("expected destroyed, got %q", final.State)
	}
	if final.DestroyedAt == nil {
		t.Fatal("expected DestroyedAt to be set")
	}
}

func TestStore_Transition_InvalidRejectsWithError(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("ws-1", "p1"))

	if _, err := s.Transition(sess.ID, StateCreated, StateRunning); err == nil {
		t.Fatal("expected error for invalid transition")
	}
}

func TestStore_Transition_StateMismatch(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("ws-1", "p1"))

	// Provide wrong fromState.
	if _, err := s.Transition(sess.ID, StateProvisioning, StateReady); err == nil {
		t.Fatal("expected error for state mismatch")
	}

	// Session should still be in StateCreated.
	got, _ := s.Get(sess.ID)
	if got.State != StateCreated {
		t.Fatalf("state should not have changed: %q", got.State)
	}
}

func TestStore_Transition_Failure(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("ws-1", "p1"))

	s.Transition(sess.ID, StateCreated, StateProvisioning)
	updated, err := s.Transition(sess.ID, StateProvisioning, StateFailed)
	if err != nil {
		t.Fatalf("expected failure transition to succeed: %v", err)
	}
	if updated.State != StateFailed {
		t.Fatalf("expected failed, got %q", updated.State)
	}
}

func TestStore_Transition_NonExistent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Transition("nonexistent-id", StateCreated, StateProvisioning); err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

// ── Workspace isolation ───────────────────────────────────────────────────────

func TestStore_WorkspaceIsolation(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create(makeSession("workspace-A", "p1"))

	// Workspace B cannot see workspace A's session.
	got, err := s.GetForWorkspace(sess.ID, "workspace-B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("workspace isolation breach: workspace-B can read workspace-A session")
	}

	// Workspace A can see it.
	got, err = s.GetForWorkspace(sess.ID, "workspace-A")
	if err != nil || got == nil {
		t.Fatalf("workspace-A should see its own session: err=%v, got=%v", err, got)
	}

	// Empty workspace skips check.
	got, err = s.GetForWorkspace(sess.ID, "")
	if err != nil || got == nil {
		t.Fatalf("empty workspace should not filter: err=%v", err)
	}
}

// ── TTL ───────────────────────────────────────────────────────────────────────

func TestStore_TTL(t *testing.T) {
	s := newTestStore(t)
	sess := makeSession("ws-1", "p1")
	sess.TTL = 5 * time.Minute

	created, err := s.Create(sess)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, _ := s.Get(created.ID)
	if got.TTL != 5*time.Minute {
		t.Fatalf("expected TTL 5m, got %v", got.TTL)
	}
}

// ── Persistence across restart ────────────────────────────────────────────────

func TestStore_PersistsAcrossRestart(t *testing.T) {
	dbPath := tempDB(t)

	s1, _ := NewStore(dbPath)
	sess, _ := s1.Create(makeSession("ws-1", "probe-a"))
	s1.Transition(sess.ID, StateCreated, StateProvisioning)
	s1.Close()

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()

	got, err := s2.Get(sess.ID)
	if err != nil || got == nil {
		t.Fatalf("session not found after restart: err=%v", err)
	}
	if got.State != StateProvisioning {
		t.Fatalf("expected provisioning after restart, got %q", got.State)
	}
}
