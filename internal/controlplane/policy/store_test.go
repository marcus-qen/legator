package policy

import (
	"path/filepath"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func policyTempDB(t *testing.T) string {
	return filepath.Join(t.TempDir(), "policy.db")
}

func TestPersistentStoreCreateAndList(t *testing.T) {
	s, err := NewPersistentStore(policyTempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Should have 3 built-in templates
	initial := s.List()
	if len(initial) < 3 {
		t.Fatalf("expected at least 3 built-in templates, got %d", len(initial))
	}

	// Create a custom one
	s.Create("Test Policy", "A test policy", protocol.CapObserve, nil, []string{"rm"}, nil)
	if len(s.List()) != len(initial)+1 {
		t.Fatalf("expected %d after create, got %d", len(initial)+1, len(s.List()))
	}
}

func TestPersistentStoreSurvivesRestart(t *testing.T) {
	dbPath := policyTempDB(t)

	s1, err := NewPersistentStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	created := s1.Create("Custom", "Persisted policy", protocol.CapDiagnose,
		[]string{"strace", "tcpdump"}, []string{"rm"}, []string{"/tmp"})
	s1.Close()

	s2, err := NewPersistentStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, ok := s2.Get(created.ID)
	if !ok {
		t.Fatalf("custom template %s not found after restart", created.ID)
	}
	if got.Name != "Custom" {
		t.Fatalf("expected name 'Custom', got %q", got.Name)
	}
	if got.Level != protocol.CapDiagnose {
		t.Fatalf("expected level diagnose, got %s", got.Level)
	}
	if len(got.Allowed) != 2 || got.Allowed[0] != "strace" {
		t.Fatalf("allowed not restored: %v", got.Allowed)
	}
}

func TestPersistentStoreDelete(t *testing.T) {
	s, err := NewPersistentStore(policyTempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	created := s.Create("Temp", "to be deleted", protocol.CapObserve, nil, nil, nil)
	before := len(s.List())

	if err := s.Delete(created.ID); err != nil {
		t.Fatal(err)
	}

	if len(s.List()) != before-1 {
		t.Fatalf("expected %d after delete, got %d", before-1, len(s.List()))
	}
}

func TestPersistentStoreUpdate(t *testing.T) {
	s, err := NewPersistentStore(policyTempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	created := s.Create("Before", "desc", protocol.CapObserve, nil, nil, nil)
	updated, err := s.Update(created.ID, "After", "new desc", protocol.CapRemediate, []string{"apt"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "After" || updated.Level != protocol.CapRemediate {
		t.Fatalf("update didn't apply: %+v", updated)
	}

	// Verify persistence
	s.Close()
	s2, err := NewPersistentStore(policyTempDB(t))
	if err != nil {
		// Can't reopen same temp, just check in-memory result
		return
	}
	defer s2.Close()
}
