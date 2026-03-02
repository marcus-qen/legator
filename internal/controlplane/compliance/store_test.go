package compliance

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreUpsertAndList(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	results := []ComplianceResult{
		{
			CheckID:   "ssh-password-auth",
			CheckName: "SSH Password Auth",
			Category:  "ssh",
			Severity:  SeverityHigh,
			ProbeID:   "probe-1",
			Status:    StatusPass,
			Evidence:  "disabled",
			Timestamp: time.Now().UTC(),
		},
		{
			CheckID:   "firewall-active",
			CheckName: "Firewall Active",
			Category:  "firewall",
			Severity:  SeverityHigh,
			ProbeID:   "probe-1",
			Status:    StatusFail,
			Evidence:  "no firewall found",
			Timestamp: time.Now().UTC(),
		},
		{
			CheckID:   "ssh-password-auth",
			CheckName: "SSH Password Auth",
			Category:  "ssh",
			Severity:  SeverityHigh,
			ProbeID:   "probe-2",
			Status:    StatusFail,
			Evidence:  "enabled",
			Timestamp: time.Now().UTC(),
		},
	}

	for _, r := range results {
		if err := store.UpsertResult(r); err != nil {
			t.Fatalf("UpsertResult(%s/%s): %v", r.ProbeID, r.CheckID, err)
		}
	}

	// List all
	all, err := store.ListResults(ResultFilter{})
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 results, got %d", len(all))
	}

	// Filter by probe
	byProbe, err := store.ListResults(ResultFilter{ProbeID: "probe-1"})
	if err != nil {
		t.Fatalf("ListResults by probe: %v", err)
	}
	if len(byProbe) != 2 {
		t.Fatalf("expected 2 results for probe-1, got %d", len(byProbe))
	}

	// Filter by status
	failing, err := store.ListResults(ResultFilter{Status: StatusFail})
	if err != nil {
		t.Fatalf("ListResults by status: %v", err)
	}
	if len(failing) != 2 {
		t.Fatalf("expected 2 failing results, got %d", len(failing))
	}

	// Filter by category
	ssh, err := store.ListResults(ResultFilter{Category: "ssh"})
	if err != nil {
		t.Fatalf("ListResults by category: %v", err)
	}
	if len(ssh) != 2 {
		t.Fatalf("expected 2 ssh results, got %d", len(ssh))
	}
}

func TestStoreUpsertUpdatesExisting(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	base := ComplianceResult{
		CheckID:   "firewall-active",
		CheckName: "Firewall Active",
		Category:  "firewall",
		Severity:  SeverityHigh,
		ProbeID:   "probe-1",
		Status:    StatusFail,
		Evidence:  "no firewall",
		Timestamp: time.Now().UTC(),
	}
	if err := store.UpsertResult(base); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	// Upsert again with different status
	base.Status = StatusPass
	base.Evidence = "nftables active"
	if err := store.UpsertResult(base); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	all, err := store.ListResults(ResultFilter{ProbeID: "probe-1"})
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	// Should still be 1 latest result
	if len(all) != 1 {
		t.Fatalf("expected 1 result after upsert, got %d", len(all))
	}
	if all[0].Status != StatusPass {
		t.Fatalf("expected status pass after update, got %s", all[0].Status)
	}

	// History should have 2 rows
	hist, err := store.History("probe-1", "firewall-active", 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 history rows, got %d", len(hist))
	}
}

func TestStoreSummary(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "compliance.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// 3 passing, 1 failing, 1 warning across 2 probes
	inputs := []ComplianceResult{
		{CheckID: "c1", CheckName: "C1", Category: "ssh", Severity: SeverityHigh, ProbeID: "p1", Status: StatusPass, Evidence: "ok", Timestamp: time.Now()},
		{CheckID: "c2", CheckName: "C2", Category: "ssh", Severity: SeverityHigh, ProbeID: "p1", Status: StatusFail, Evidence: "bad", Timestamp: time.Now()},
		{CheckID: "c1", CheckName: "C1", Category: "ssh", Severity: SeverityHigh, ProbeID: "p2", Status: StatusPass, Evidence: "ok", Timestamp: time.Now()},
		{CheckID: "c3", CheckName: "C3", Category: "firewall", Severity: SeverityHigh, ProbeID: "p2", Status: StatusPass, Evidence: "ok", Timestamp: time.Now()},
		{CheckID: "c4", CheckName: "C4", Category: "firewall", Severity: SeverityHigh, ProbeID: "p1", Status: StatusWarning, Evidence: "partial", Timestamp: time.Now()},
	}
	for _, r := range inputs {
		if err := store.UpsertResult(r); err != nil {
			t.Fatalf("UpsertResult: %v", err)
		}
	}

	summary, err := store.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	if summary.TotalChecks != 5 {
		t.Errorf("expected 5 total checks, got %d", summary.TotalChecks)
	}
	if summary.Passing != 3 {
		t.Errorf("expected 3 passing, got %d", summary.Passing)
	}
	if summary.Failing != 1 {
		t.Errorf("expected 1 failing, got %d", summary.Failing)
	}
	if summary.Warning != 1 {
		t.Errorf("expected 1 warning, got %d", summary.Warning)
	}
	if summary.TotalProbes != 2 {
		t.Errorf("expected 2 probes, got %d", summary.TotalProbes)
	}

	// Score = 3 passing / (3+1+1) = 60%
	if summary.ScorePct < 59.9 || summary.ScorePct > 60.1 {
		t.Errorf("expected score ~60%%, got %.1f", summary.ScorePct)
	}

	if _, ok := summary.ByCategory["ssh"]; !ok {
		t.Error("expected ssh category in summary")
	}
	if _, ok := summary.ByCategory["firewall"]; !ok {
		t.Error("expected firewall category in summary")
	}
}
