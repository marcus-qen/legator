package discovery

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestCandidateStore(t *testing.T) *CandidateStore {
	t.Helper()
	db := openTestDB(t)
	cs, err := NewCandidateStore(db)
	if err != nil {
		t.Fatalf("new candidate store: %v", err)
	}
	return cs
}

func TestCandidateStoreUpsert(t *testing.T) {
	cs := newTestCandidateStore(t)

	c := &DeployCandidate{
		SourceProbe: "probe-1",
		IP:          "192.168.1.10",
		Port:        22,
		SSHBanner:   "SSH-2.0-OpenSSH_8.4",
		OSGuess:     "linux",
	}

	got, err := cs.Upsert(c)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got.ID == "" {
		t.Fatal("expected ID to be set")
	}
	if got.Status != CandidateStatusDiscovered {
		t.Fatalf("expected status discovered, got %q", got.Status)
	}
	if got.IP != "192.168.1.10" {
		t.Fatalf("expected IP 192.168.1.10, got %q", got.IP)
	}
}

func TestCandidateStoreUpsertDedup(t *testing.T) {
	cs := newTestCandidateStore(t)

	c := &DeployCandidate{
		SourceProbe: "probe-1",
		IP:          "10.0.0.5",
		Port:        22,
	}
	first, err := cs.Upsert(c)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Second upsert with a different probe — should update probe but keep status.
	c2 := &DeployCandidate{
		SourceProbe: "probe-2",
		IP:          "10.0.0.5",
		Port:        22,
		SSHBanner:   "SSH-2.0-OpenSSH_9.0",
	}
	second, err := cs.Upsert(c2)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same ID on dedup, got %q != %q", second.ID, first.ID)
	}
	if second.SourceProbe != "probe-2" {
		t.Fatalf("expected source probe updated to probe-2, got %q", second.SourceProbe)
	}
	if second.SSHBanner != "SSH-2.0-OpenSSH_9.0" {
		t.Fatalf("expected banner updated, got %q", second.SSHBanner)
	}
}

func TestCandidateStoreGet(t *testing.T) {
	cs := newTestCandidateStore(t)

	_, err := cs.Get("nonexistent")
	if !IsCandidateNotFound(err) {
		t.Fatalf("expected not-found error, got %v", err)
	}

	c := &DeployCandidate{SourceProbe: "probe-1", IP: "10.0.0.1", Port: 22}
	inserted, err := cs.Upsert(c)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := cs.Get(inserted.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.IP != "10.0.0.1" {
		t.Fatalf("expected IP 10.0.0.1, got %q", got.IP)
	}
}

func TestCandidateStoreList(t *testing.T) {
	cs := newTestCandidateStore(t)

	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		if _, err := cs.Upsert(&DeployCandidate{SourceProbe: "p", IP: ip, Port: 22}); err != nil {
			t.Fatalf("upsert %s: %v", ip, err)
		}
	}

	all, err := cs.List("")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(all))
	}

	disc, err := cs.List(CandidateStatusDiscovered)
	if err != nil {
		t.Fatalf("list discovered: %v", err)
	}
	if len(disc) != 3 {
		t.Fatalf("expected 3 discovered, got %d", len(disc))
	}

	approved, err := cs.List(CandidateStatusApproved)
	if err != nil {
		t.Fatalf("list approved: %v", err)
	}
	if len(approved) != 0 {
		t.Fatalf("expected 0 approved, got %d", len(approved))
	}
}

func TestCandidateStoreTransitions(t *testing.T) {
	cs := newTestCandidateStore(t)

	c := &DeployCandidate{SourceProbe: "p", IP: "10.1.1.1", Port: 22, ReportedAt: time.Now()}
	inserted, err := cs.Upsert(c)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	id := inserted.ID

	// discovered → approved
	if err := cs.Transition(id, CandidateStatusApproved, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// approved → deploying
	if err := cs.Transition(id, CandidateStatusDeploying, ""); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	// deploying → deployed
	if err := cs.Transition(id, CandidateStatusDeployed, ""); err != nil {
		t.Fatalf("deployed: %v", err)
	}

	got, _ := cs.Get(id)
	if got.Status != CandidateStatusDeployed {
		t.Fatalf("expected deployed, got %q", got.Status)
	}
}

func TestCandidateStoreInvalidTransition(t *testing.T) {
	cs := newTestCandidateStore(t)

	c := &DeployCandidate{SourceProbe: "p", IP: "10.2.2.2", Port: 22}
	inserted, _ := cs.Upsert(c)

	// discovered → deployed (not allowed; must go through approved/deploying)
	err := cs.Transition(inserted.ID, CandidateStatusDeployed, "")
	if err == nil {
		t.Fatal("expected error for invalid transition")
	}
	if !isInvalidTransition(err) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestCandidateStoreRejection(t *testing.T) {
	cs := newTestCandidateStore(t)

	c := &DeployCandidate{SourceProbe: "p", IP: "10.3.3.3", Port: 22}
	inserted, _ := cs.Upsert(c)

	if err := cs.Transition(inserted.ID, CandidateStatusRejected, ""); err != nil {
		t.Fatalf("reject: %v", err)
	}

	got, _ := cs.Get(inserted.ID)
	if got.Status != CandidateStatusRejected {
		t.Fatalf("expected rejected, got %q", got.Status)
	}

	// rejected → any: not allowed
	err := cs.Transition(inserted.ID, CandidateStatusApproved, "")
	if err == nil {
		t.Fatal("expected error transitioning out of rejected")
	}
}

func TestCandidateStoreFailedCanBeReApproved(t *testing.T) {
	cs := newTestCandidateStore(t)

	c := &DeployCandidate{SourceProbe: "p", IP: "10.4.4.4", Port: 22}
	inserted, _ := cs.Upsert(c)
	id := inserted.ID

	_ = cs.Transition(id, CandidateStatusApproved, "")
	_ = cs.Transition(id, CandidateStatusDeploying, "")
	_ = cs.Transition(id, CandidateStatusFailed, "network error")

	// failed → approved (re-approval)
	if err := cs.Transition(id, CandidateStatusApproved, ""); err != nil {
		t.Fatalf("re-approve after failure: %v", err)
	}
	got, _ := cs.Get(id)
	if got.Status != CandidateStatusApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}
}

func TestCandidateStoreTransitionNotFound(t *testing.T) {
	cs := newTestCandidateStore(t)
	err := cs.Transition("nonexistent-id", CandidateStatusApproved, "")
	if !IsCandidateNotFound(err) {
		t.Fatalf("expected not-found, got %v", err)
	}
}

func TestCandidateStoreOpenFromDiscoveryStore(t *testing.T) {
	store := newTestStore(t)

	cs, err := store.OpenCandidateStore()
	if err != nil {
		t.Fatalf("open candidate store: %v", err)
	}

	_, err = cs.Upsert(&DeployCandidate{SourceProbe: "p", IP: "172.16.0.1", Port: 22})
	if err != nil {
		t.Fatalf("upsert via shared store: %v", err)
	}
}
