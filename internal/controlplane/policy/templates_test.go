package policy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestNewStoreHasBuiltins(t *testing.T) {
	s := NewStore()
	list := s.List()
	if len(list) < 3 {
		t.Fatalf("expected at least 3 built-in templates, got %d", len(list))
	}
	obs, ok := s.Get("observe-only")
	if !ok || obs.Level != protocol.CapObserve {
		t.Fatal("observe-only template missing or wrong level")
	}
}

func TestCreateAndGet(t *testing.T) {
	s := NewStore()
	tpl := s.Create("Custom", "A custom policy", protocol.CapDiagnose, nil, []string{"rm"}, nil)
	if tpl.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	got, ok := s.Get(tpl.ID)
	if !ok {
		t.Fatal("template not found after create")
	}
	if got.Name != "Custom" || got.Level != protocol.CapDiagnose {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestUpdate(t *testing.T) {
	s := NewStore()
	tpl := s.Create("Test", "desc", protocol.CapObserve, nil, nil, nil)
	updated, err := s.Update(tpl.ID, "Test v2", "new desc", protocol.CapRemediate, []string{"ls"}, []string{"rm"}, []string{"/etc"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Test v2" || updated.Level != protocol.CapRemediate {
		t.Fatalf("update failed: %#v", updated)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := NewStore()
	_, err := s.Update("nonexistent", "", "", "", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestDelete(t *testing.T) {
	s := NewStore()
	tpl := s.Create("Temp", "", protocol.CapObserve, nil, nil, nil)
	if err := s.Delete(tpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(tpl.ID); ok {
		t.Fatal("template should be deleted")
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := NewStore()
	if err := s.Delete("nope"); err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestToPolicy(t *testing.T) {
	s := NewStore()
	obs, _ := s.Get("observe-only")
	pol := obs.ToPolicy()
	if pol.PolicyID != "observe-only" || pol.Level != protocol.CapObserve {
		t.Fatalf("unexpected policy: %#v", pol)
	}
	if len(pol.Blocked) == 0 {
		t.Fatal("expected blocked commands from observe-only")
	}
}
