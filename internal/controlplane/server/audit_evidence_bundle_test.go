package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/compliance"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestHandleAuditEvidenceBundleExport_BundleContentsAndManifest(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-a", "host-a", "linux", "amd64")
	srv.fleetMgr.Register("probe-b", "host-b", "linux", "amd64")

	base := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	srv.recordAudit(audit.Event{
		Timestamp: base.Add(1 * time.Minute),
		Type:      audit.EventPolicyChanged,
		ProbeID:   "probe-a",
		Actor:     "alice",
		Summary:   "policy changed",
		Before:    map[string]any{"level": "observe"},
		After:     map[string]any{"level": "remediate"},
	})
	srv.recordAudit(audit.Event{
		Timestamp: base.Add(2 * time.Minute),
		Type:      audit.EventCommandSent,
		ProbeID:   "probe-b",
		Actor:     "bob",
		Summary:   "command dispatched",
	})

	if err := srv.complianceStore.UpsertResult(compliance.ComplianceResult{
		CheckID:   "ssh-root-login",
		CheckName: "SSH Root Login",
		Category:  "cis",
		Severity:  compliance.SeverityCritical,
		ProbeID:   "probe-a",
		Status:    compliance.StatusPass,
		Evidence:  "PermitRootLogin no",
		Timestamp: base.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("upsert compliance probe-a: %v", err)
	}
	if err := srv.complianceStore.UpsertResult(compliance.ComplianceResult{
		CheckID:   "firewall-active",
		CheckName: "Firewall Active",
		Category:  "soc2",
		Severity:  compliance.SeverityHigh,
		ProbeID:   "probe-b",
		Status:    compliance.StatusFail,
		Evidence:  "no rules",
		Timestamp: base.Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("upsert compliance probe-b: %v", err)
	}

	cmd := &protocol.CommandPayload{Command: "apt upgrade", Args: []string{"-y"}}
	reqA, err := srv.approvalQueue.Submit("probe-a", cmd, "maintenance", "high", "api")
	if err != nil {
		t.Fatalf("submit approval probe-a: %v", err)
	}
	if _, err := srv.approvalQueue.Decide(reqA.ID, approval.DecisionApproved, "alice"); err != nil {
		t.Fatalf("decide approval probe-a: %v", err)
	}
	if _, err := srv.approvalQueue.Submit("probe-b", cmd, "maintenance", "high", "api"); err != nil {
		t.Fatalf("submit approval probe-b: %v", err)
	}

	url := fmt.Sprintf(
		"/api/v1/audit/export/bundle?since=%s&until=%s&framework=cis&probe_ids=probe-a",
		base.Add(-1*time.Minute).Format(time.RFC3339Nano),
		time.Now().UTC().Add(1*time.Minute).Format(time.RFC3339Nano),
	)
	httpReq := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	srv.handleAuditEvidenceBundleExport(rr, httpReq)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("expected application/zip, got %q", got)
	}

	files := unzipBytes(t, rr.Body.Bytes())
	for _, name := range []string{
		"audit-log.jsonl",
		"inventory-snapshots.json",
		"compliance-check-results.json",
		"change-diffs.jsonl",
		"approval-records.json",
		"manifest.json",
	} {
		if _, ok := files[name]; !ok {
			t.Fatalf("missing zip entry %s", name)
		}
	}

	var manifest evidenceBundleManifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.GeneratedAt == "" {
		t.Fatal("manifest missing generated_at")
	}
	if manifest.Filters.Framework != "cis" {
		t.Fatalf("expected framework=cis, got %q", manifest.Filters.Framework)
	}
	if len(manifest.Files) != 5 {
		t.Fatalf("expected 5 file checksums, got %d", len(manifest.Files))
	}
	for _, digest := range manifest.Files {
		content, ok := files[digest.Name]
		if !ok {
			t.Fatalf("manifest references missing file %s", digest.Name)
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != digest.SHA256 {
			t.Fatalf("checksum mismatch for %s: got %s want %s", digest.Name, got, digest.SHA256)
		}
	}

	var complianceSnapshot evidenceComplianceSnapshot
	if err := json.Unmarshal(files["compliance-check-results.json"], &complianceSnapshot); err != nil {
		t.Fatalf("decode compliance snapshot: %v", err)
	}
	if complianceSnapshot.Total != 1 {
		t.Fatalf("expected 1 compliance result after framework/probe filtering, got %d", complianceSnapshot.Total)
	}
	if complianceSnapshot.Results[0].ProbeID != "probe-a" {
		t.Fatalf("expected compliance probe-a, got %s", complianceSnapshot.Results[0].ProbeID)
	}

	var approvalSnapshot evidenceApprovalSnapshot
	if err := json.Unmarshal(files["approval-records.json"], &approvalSnapshot); err != nil {
		t.Fatalf("decode approval snapshot: %v", err)
	}
	if approvalSnapshot.Total != 1 {
		t.Fatalf("expected 1 approval record after probe filtering, got %d", approvalSnapshot.Total)
	}
	if approvalSnapshot.Approvals[0].ProbeID != "probe-a" {
		t.Fatalf("expected approval probe-a, got %s", approvalSnapshot.Approvals[0].ProbeID)
	}

	auditLines := splitJSONLines(files["audit-log.jsonl"])
	if len(auditLines) == 0 {
		t.Fatal("expected at least one audit line")
	}
	for _, line := range auditLines {
		var evt audit.Event
		if err := json.Unmarshal(line, &evt); err != nil {
			t.Fatalf("decode audit line: %v", err)
		}
		if evt.ProbeID != "probe-a" {
			t.Fatalf("expected audit probe-a after filtering, got %s", evt.ProbeID)
		}
	}

	if len(splitJSONLines(files["change-diffs.jsonl"])) == 0 {
		t.Fatal("expected change-diffs.jsonl to include at least one diff event")
	}
}

func TestHandleAuditEvidenceBundleExport_AuditsSuccessAndFailure(t *testing.T) {
	srv := newTestServer(t)

	successReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit/export/bundle", nil)
	successRR := httptest.NewRecorder()
	srv.handleAuditEvidenceBundleExport(successRR, successReq)
	if successRR.Code != http.StatusOK {
		t.Fatalf("expected success export status 200, got %d", successRR.Code)
	}

	failureReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit/export/bundle?since=bad-timestamp", nil)
	failureRR := httptest.NewRecorder()
	srv.handleAuditEvidenceBundleExport(failureRR, failureReq)
	if failureRR.Code != http.StatusBadRequest {
		t.Fatalf("expected failure export status 400, got %d", failureRR.Code)
	}

	events, err := srv.auditStore.QueryPersisted(audit.Filter{Type: audit.EventAuditEvidenceBundleExport})
	if err != nil {
		t.Fatalf("query audit evidence export events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 audit evidence export events, got %d", len(events))
	}

	haveSuccess := false
	haveFailure := false
	for _, evt := range events {
		detail, _ := evt.Detail.(map[string]any)
		status, _ := detail["status"].(string)
		errMsg, _ := detail["error"].(string)
		if status == "success" {
			haveSuccess = true
		}
		if status == "failure" && strings.Contains(errMsg, "invalid since timestamp") {
			haveFailure = true
		}
	}

	if !haveSuccess {
		t.Fatal("missing success audit record for evidence bundle export")
	}
	if !haveFailure {
		t.Fatal("missing failure audit record for evidence bundle export")
	}
}

func unzipBytes(t *testing.T, payload []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", f.Name, err)
		}
		data := &bytes.Buffer{}
		if _, err := data.ReadFrom(rc); err != nil {
			_ = rc.Close()
			t.Fatalf("read zip entry %s: %v", f.Name, err)
		}
		_ = rc.Close()
		files[f.Name] = data.Bytes()
	}
	return files
}

func splitJSONLines(data []byte) [][]byte {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "\n")
	out := make([][]byte, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, []byte(part))
	}
	return out
}
