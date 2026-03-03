package policy

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/migration"
	"github.com/marcus-qen/legator/internal/protocol"
	_ "modernc.org/sqlite"
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

	initial := s.List()
	if len(initial) < 3 {
		t.Fatalf("expected at least 3 built-in templates, got %d", len(initial))
	}

	created := s.Create("Test Policy", "A test policy", protocol.CapObserve, nil, []string{"rm"}, nil, TemplateOptions{})
	if len(s.List()) != len(initial)+1 {
		t.Fatalf("expected %d after create, got %d", len(initial)+1, len(s.List()))
	}
	if created.ExecutionClassRequired != protocol.ExecObserveDirect || created.ApprovalMode != protocol.ApprovalNone {
		t.Fatalf("expected default v2 values for observe level, got %+v", created)
	}
}

func TestPersistentStoreSurvivesRestartWithV2Fields(t *testing.T) {
	dbPath := policyTempDB(t)

	s1, err := NewPersistentStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	created := s1.Create("Custom", "Persisted policy", protocol.CapDiagnose,
		[]string{"strace", "tcpdump"}, []string{"rm"}, []string{"/tmp"},
		TemplateOptions{
			ExecutionClassRequired:   protocol.ExecBreakglassDirect,
			SandboxRequired:          true,
			ApprovalMode:             protocol.ApprovalPlanFirst,
			RequireSecondApprover:    true,
			RequireSecondApproverSet: true,
			Breakglass: protocol.BreakglassPolicy{
				Enabled:                  true,
				AllowedReasons:           []string{"incident_response"},
				RequireTypedConfirmation: true,
			},
			MaxRuntimeSec: 300,
			AllowedScopes: []string{"fleet.read", "command.exec"},
		})
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

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
	if got.ExecutionClassRequired != protocol.ExecBreakglassDirect || got.ApprovalMode != protocol.ApprovalPlanFirst {
		t.Fatalf("v2 fields not restored: %+v", got)
	}
	if !got.RequireSecondApprover {
		t.Fatalf("require_second_approver not restored: %+v", got)
	}
	if !got.Breakglass.Enabled || len(got.Breakglass.AllowedReasons) != 1 {
		t.Fatalf("breakglass not restored: %+v", got.Breakglass)
	}
	if got.MaxRuntimeSec != 300 {
		t.Fatalf("max_runtime_sec not restored: %d", got.MaxRuntimeSec)
	}
	if len(got.AllowedScopes) != 2 || got.AllowedScopes[0] != "fleet.read" {
		t.Fatalf("allowed_scopes not restored: %v", got.AllowedScopes)
	}
}

func TestPersistentStoreDelete(t *testing.T) {
	s, err := NewPersistentStore(policyTempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	created := s.Create("Temp", "to be deleted", protocol.CapObserve, nil, nil, nil, TemplateOptions{})
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

	created := s.Create("Before", "desc", protocol.CapObserve, nil, nil, nil, TemplateOptions{})
	updated, err := s.Update(created.ID, "After", "new desc", protocol.CapRemediate, []string{"apt"}, nil, nil, TemplateOptions{ApprovalMode: protocol.ApprovalEveryAction})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "After" || updated.Level != protocol.CapRemediate {
		t.Fatalf("update didn't apply: %+v", updated)
	}
	if updated.ApprovalMode != protocol.ApprovalEveryAction {
		t.Fatalf("expected approval mode override, got %q", updated.ApprovalMode)
	}
}

func TestPersistentStoreMigrationBackfillsV2FromLegacyLevel(t *testing.T) {
	dbPath := policyTempDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`CREATE TABLE policy_templates (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		level       TEXT NOT NULL,
		allowed     TEXT NOT NULL DEFAULT '[]',
		blocked     TEXT NOT NULL DEFAULT '[]',
		paths       TEXT NOT NULL DEFAULT '[]',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy table: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO policy_templates (id, name, description, level, allowed, blocked, paths, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-diagnose", "Legacy Diagnose", "legacy", "diagnose", "[]", "[]", "[]", now, now); err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := migration.SetVersion(db, 1); err != nil {
		_ = db.Close()
		t.Fatalf("set schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewPersistentStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tpl, ok := store.Get("legacy-diagnose")
	if !ok {
		t.Fatal("expected migrated legacy template")
	}
	if tpl.ExecutionClassRequired != protocol.ExecDiagnoseSandbox {
		t.Fatalf("expected diagnose execution class, got %q", tpl.ExecutionClassRequired)
	}
	if !tpl.SandboxRequired {
		t.Fatal("expected sandbox_required=true for diagnose legacy row")
	}
	if tpl.ApprovalMode != protocol.ApprovalMutationGate {
		t.Fatalf("expected mutation_gate approval mode, got %q", tpl.ApprovalMode)
	}
}
