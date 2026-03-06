# GAP-5 Compliance Golden-Path E2E Validation (Live)

- **Date (UTC):** 2026-03-06
- **Branch:** `agent/gap5-compliance-e2e-validation`
- **Target:** `https://legator.lab.k-dev.uk`
- **Artifact root:** `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z`

## Scope
Validate GAP-5 compliance golden path on live probes:
1. Trigger compliance scan
2. Verify non-skipped results
3. Verify summary metrics align with raw results
4. Verify CSV/PDF exports generate and download

## Smoke Tests (Before)

### 1) Branch and HEAD
```bash
git branch --show-current
git rev-parse HEAD
```
Output:
```text
agent/gap5-compliance-e2e-validation
fefc766bbe39f6bdc3622a887a004696076c45dd
```

### 2) API reachable with auth
```bash
curl -sk -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer <admin-key>" \
  https://legator.lab.k-dev.uk/api/v1/probes
```
Output:
```text
200
```

## E2E Validation Execution

### A) Trigger compliance scan on live online probes
Online probes selected (6):
- `prb-51fd13f7`
- `prb-58d861dd`
- `prb-63655638`
- `prb-637eb583`
- `prb-6c26d197`
- `prb-7fb28227`

Command:
```bash
curl -sk -D projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan.headers \
  -o projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan-response.json \
  -w "%{http_code}" \
  -X POST \
  -H "Authorization: Bearer <admin-key>" \
  -H "Content-Type: application/json" \
  --data @projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan-request.json \
  https://legator.lab.k-dev.uk/api/v1/compliance/scan
```
Output:
```text
200
scan_id: c6df4924-2560-439c-8274-e2e5657076fc
total_results: 42
status_breakdown: skipped=42
```

Second confirmation attempt (single probe):
```bash
curl -sk -X POST \
  -H "Authorization: Bearer <admin-key>" \
  -H "Content-Type: application/json" \
  https://legator.lab.k-dev.uk/api/v1/compliance/scan \
  --data '{"probe_ids":["prb-63655638"]}'
```
Output excerpt:
```text
scan_id: 6d8b2c95-63e4-4da0-9f49-318a60ad80c3
total_results: 7
status_breakdown: skipped=7
sample_evidence: Probe prb-63655638 (castra) is not accessible for remote execution (status: online, type: agent)
```

**Result:** non-skipped acceptance criterion is **NOT met** (all checks skipped).

### B) Verify summary metrics align with raw results
Commands:
```bash
curl -sk -H "Authorization: Bearer <admin-key>" \
  "https://legator.lab.k-dev.uk/api/v1/compliance/results?limit=5000" \
  > projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/results.json

curl -sk -H "Authorization: Bearer <admin-key>" \
  "https://legator.lab.k-dev.uk/api/v1/compliance/summary" \
  > projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/summary.json
```
Computed comparison (`summary-compare.json`) and scan-local comparison (`scan-summary-compare.json`) both returned `matches: true`.

Output excerpt:
```json
{
  "summary": {
    "total_checks": 28,
    "passing": 0,
    "failing": 0,
    "warning": 0,
    "unknown": 28,
    "total_probes": 4,
    "score_pct": 0
  },
  "calculated_from_results": {
    "total_checks": 28,
    "passing": 0,
    "failing": 0,
    "warning": 0,
    "unknown": 28
  },
  "matches": true
}
```

### C) Verify CSV export generated/downloadable
Command:
```bash
curl -sk -D projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/export-csv.headers \
  -o projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/compliance-export.csv \
  -w "%{http_code}" \
  -H "Authorization: Bearer <admin-key>" \
  "https://legator.lab.k-dev.uk/api/v1/compliance/export/csv?probes=prb-51fd13f7,prb-58d861dd,prb-63655638,prb-637eb583,prb-6c26d197,prb-7fb28227"
```
Output:
```text
200
csv_bytes=5588
csv_lines=29
```
CSV head confirms data rows present.

### D) Verify PDF export generated/downloadable
Command:
```bash
curl -sk -D projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/export-pdf.headers \
  -o projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/compliance-export.pdf \
  -w "%{http_code}" \
  -H "Authorization: Bearer <admin-key>" \
  "https://legator.lab.k-dev.uk/api/v1/compliance/export/pdf?probes=prb-51fd13f7,prb-58d861dd,prb-63655638,prb-637eb583,prb-6c26d197,prb-7fb28227&scope=live+online+probes"
```
Output:
```text
200
pdf_bytes=6743
pdf_magic=25504446  (%PDF)
```

## Evidence Artifacts

- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan-request.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan-response.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan-response-attempt2.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan-summary.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/results.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/summary.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/summary-compare.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan-summary-compare.json`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/compliance-export.csv`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/compliance-export.pdf`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/scan.headers`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/export-csv.headers`
- `projects/dev-lab/research/artifacts/gap5-compliance-e2e-20260306T155422Z/export-pdf.headers`

## GAP-5 Readiness Verdict

- ✅ API reachable with auth
- ✅ Scan endpoint callable
- ✅ Summary counters consistent with raw results
- ✅ CSV/PDF export endpoints generate downloadable artifacts
- ❌ **Non-skipped live compliance results not achieved** (all results skipped)

**Overall:** GAP-5 is **NOT ready to close**.

## Recommendation for Issue #28 Disposition

Issue #28 (`Compliance scanner doesn't execute checks on agent-type probes`) should remain **open** (or be **re-opened** if closed), because live validation still reproduces the same failure mode: all compliance checks return `skipped` on agent probes.

## Card Context Update

IdeaVault card `f7c3ac60-7d19-490d-808e-8c825f6fbb90` (**GAP-5**) was annotated with this validation outcome and recommendation to keep issue #28 open until non-skipped live results are observed.
