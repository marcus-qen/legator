# MCP Tool & Resource Reference

Legator exposes a [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server over SSE transport. AI assistants (Claude, Cursor, etc.) can connect to inspect fleet state, run commands, manage approvals, and query jobs without using the REST API directly.

---

## Transport

**Endpoint:** `GET /mcp` and `POST /mcp`  
**Protocol:** Server-Sent Events (SSE)  
**Content-Type:** `text/event-stream`

**Auth:** Same as REST — `Authorization: Bearer lgk_<64hex>` or a valid session cookie.

**Connect example:**

```bash
curl -N -H "Authorization: Bearer lgk_..." \
     -H "Content-Type: application/json" \
     https://legator.example.com/mcp
```

MCP clients (e.g. Claude Desktop, MCP SDK) configure the server as:

```json
{
  "mcpServers": {
    "legator": {
      "url": "https://legator.example.com/mcp",
      "headers": {"Authorization": "Bearer lgk_..."}
    }
  }
}
```

---

## Tools

Tools are callable functions. All tool calls require the caller to have at least `PermFleetRead`. Write operations require `PermFleetWrite` or `PermCommandExec`.

---

### `legator_list_probes`

**Description:** List probes in the Legator fleet with status/tag filtering.

**Input schema:**
```json
{
  "status": "online | offline | all",
  "tag": "optional tag filter"
}
```

**Output:** JSON array of probe summaries.
```json
[
  {
    "id": "prb-a1b2c3d4",
    "hostname": "web-01",
    "status": "online",
    "tags": ["web", "prod"],
    "last_seen": "2026-03-01T23:00:00Z"
  }
]
```

---

### `legator_probe_info`

**Description:** Get detailed state for a specific probe.

**Input schema:**
```json
{"probe_id": "prb-a1b2c3d4"}
```

**Output:** Full `ProbeState` object including inventory, health score, policy level, tags, registered time.

---

### `legator_run_command`

**Description:** Run a command on a probe and wait for the result.

**Input schema:**
```json
{
  "probe_id": "prb-a1b2c3d4",
  "command": "df -h"
}
```

**Output:** Command result with stdout/stderr or approval/denial status.
```json
{
  "status": "complete",
  "request_id": "req-abc123",
  "output": "Filesystem  Size  Used ...",
  "exit_code": 0
}
```

If the probe's policy requires approval, returns pending status with `approval_id`.

---

### `legator_get_inventory`

**Description:** Get system inventory for a specific probe.

**Input schema:**
```json
{"probe_id": "prb-a1b2c3d4"}
```

**Output:** Inventory object.
```json
{
  "hostname": "web-01",
  "os": {"name": "Ubuntu", "version": "22.04"},
  "cpu": {"cores": 4, "model": "Intel Xeon"},
  "ram": {"total_bytes": 8589934592},
  "disks": [...],
  "network": [...]
}
```

---

### `legator_fleet_query`

**Description:** Answer a natural-language fleet query using summary stats.

**Input schema:**
```json
{"question": "How many probes are online in the prod tag?"}
```

**Output:** Plain-text summary with fleet counts, tag breakdown, and inventory aggregates.

---

### `legator_federation_inventory`

**Description:** Get federated inventory across sources with filtering support.

**Input schema:**
```json
{
  "tag": "optional",
  "status": "online | offline | degraded | pending",
  "source": "optional source id/name",
  "cluster": "optional cluster",
  "site": "optional site",
  "search": "optional free-text search",
  "tenant_id": "optional tenant scope",
  "org_id": "optional org scope",
  "scope_id": "optional federation scope"
}
```

**Output:** Federation inventory with sources, probes, and consistency indicators.

**Permission:** PermFleetRead. Restricted by API key's federation access scope.

---

### `legator_federation_summary`

**Description:** Get federated source health/aggregate rollups with filtering support.

**Input schema:** Same as `legator_federation_inventory`.

**Output:** Federation summary with aggregate probe counts, source health, and consistency.

**Permission:** PermFleetRead. Restricted by API key's federation access scope.

---

### `legator_search_audit`

**Description:** Search Legator audit events.

**Input schema:**
```json
{
  "probe_id": "optional probe filter",
  "type": "optional event type (e.g. command.sent)",
  "since": "2026-03-01T00:00:00Z",
  "limit": 50
}
```

**Output:** Array of audit events.
```json
[
  {
    "id": "evt-abc",
    "timestamp": "2026-03-01T23:00:00Z",
    "type": "command.sent",
    "probe_id": "prb-a1b2c3d4",
    "actor": "alice",
    "summary": "Command dispatched: df -h"
  }
]
```

---

### `legator_decide_approval`

**Description:** Approve or deny a pending approval request and dispatch on approve.

**Input schema:**
```json
{
  "approval_id": "apr-xyz",
  "decision": "approved | denied",
  "decided_by": "alice"
}
```

**Output:**
```json
{"status": "dispatched", "request_id": "req-abc123"}
```
or
```json
{"status": "denied"}
```

---

### `legator_probe_health`

**Description:** Get health score/status/warnings for a probe.

**Input schema:**
```json
{"probe_id": "prb-a1b2c3d4"}
```

**Output:**
```json
{"score": 92, "status": "healthy", "warnings": []}
```

---

### `legator_list_jobs`

**Description:** List configured scheduled jobs.

**Input schema:** `{}` (no inputs)

**Output:** Array of job objects with schedule, last run, status.

---

### `legator_list_job_runs`

**Description:** List job runs with optional status/time filters.

**Input schema:**
```json
{
  "job_id": "optional",
  "probe_id": "optional",
  "status": "queued | pending | running | success | failed | canceled | denied",
  "started_after": "2026-03-01T00:00:00Z",
  "started_before": "2026-03-01T23:59:59Z",
  "limit": 50
}
```

**Output:**
```json
{
  "runs": [...],
  "count": 12,
  "failed_count": 2,
  "success_count": 9,
  "running_count": 1,
  "pending_count": 0,
  "queued_count": 0,
  "canceled_count": 0,
  "denied_count": 0
}
```

---

### `legator_get_job_run`

**Description:** Get details for a specific job run.

**Input schema:**
```json
{"run_id": "run-xyz"}
```

**Output:**
```json
{
  "run": {
    "id": "run-xyz",
    "job_id": "job-abc",
    "status": "success",
    "started_at": "...",
    "finished_at": "...",
    "execution_id": "exec-123",
    "attempt": 1,
    "output": "..."
  },
  "active": false,
  "terminal": true
}
```

---

### `legator_poll_job_active`

**Description:** Poll active status for a scheduled job until terminal or timeout.

**Input schema:**
```json
{
  "job_id": "job-abc",
  "wait_ms": 10000,
  "interval_ms": 500,
  "include_job": false,
  "include_runs": true
}
```

- `wait_ms`: max poll window (default 10000, max 60000)
- `interval_ms`: poll interval (default 500, min 100)
- `include_runs`: include active run payloads (default true)

**Output:**
```json
{
  "job_id": "job-abc",
  "polled_at": "2026-03-01T23:00:00Z",
  "completed": true,
  "active": false,
  "active_runs": 0,
  "runs": []
}
```

---

### `legator_stream_job_run_output`

**Description:** Stream incremental output chunks for a running job run.

**Input schema:**
```json
{
  "run_id": "run-xyz",
  "wait_ms": 5000,
  "max_chunks": 256
}
```

- `wait_ms`: max wait for chunks (default 5000, max 60000)
- `max_chunks`: max chunks before returning (default 256, max 1024)

**Output:**
```json
{
  "run_id": "run-xyz",
  "request_id": "req-abc",
  "status": "running",
  "terminal": false,
  "chunks": [
    {"chunk": "Starting backup...", "final": false},
    {"chunk": "Done.", "final": true}
  ],
  "buffered_output": "Starting backup...\nDone."
}
```

---

### `legator_stream_job_events`

**Description:** Stream or poll job lifecycle events using audit/event bus infrastructure.

**Input schema:**
```json
{
  "job_id": "optional",
  "run_id": "optional",
  "execution_id": "optional",
  "request_id": "optional",
  "probe_id": "optional",
  "wait_ms": 0,
  "limit": 50,
  "since": "2026-03-01T00:00:00Z"
}
```

If `wait_ms > 0` and no historical events match, the tool subscribes to the live event bus and waits up to `wait_ms` for matching events.

**Output:**
```json
{
  "events": [
    {
      "timestamp": "2026-03-01T23:00:00Z",
      "type": "job.run.succeeded",
      "summary": "...",
      "detail": {"job_id": "job-abc", "run_id": "run-xyz", "execution_id": "exec-123"}
    }
  ],
  "count": 1,
  "polled_at": "2026-03-01T23:00:05Z"
}
```

---

### Conditional Tools (Grafana adapter)

Available only when `LEGATOR_GRAFANA_ENABLED=true`.

#### `legator_grafana_status`
**Description:** Get Grafana adapter status (read-only capacity availability summary).  
**Input:** `{}` — **Permission:** PermFleetRead

#### `legator_grafana_snapshot`
**Description:** Get Grafana adapter capacity snapshot.  
**Input:** `{}` — **Permission:** PermFleetRead

#### `legator_grafana_capacity_policy`
**Description:** Get Grafana-derived capacity signals plus policy rationale projection.  
**Input:** `{}` — **Permission:** PermFleetRead  
**Output:**
```json
{
  "capacity": {
    "source": "grafana",
    "availability": 0.98,
    "dashboard_coverage": 0.9,
    "query_coverage": 0.95,
    "datasource_count": 4,
    "partial": false,
    "warnings": []
  },
  "policy_decision": "allow",
  "policy_rationale": {
    "summary": "Capacity signals healthy",
    "drove_outcome": true
  }
}
```

---

### Conditional Tools (Kubeflow adapter)

Available only when `LEGATOR_KUBEFLOW_ENABLED=true` and actions enabled.

#### `legator_kubeflow_run_status`
**Description:** Get Kubeflow run/job status from the control-plane adapter.  
**Input:**
```json
{"name": "training-run-1", "kind": "runs.kubeflow.org", "namespace": "kubeflow"}
```

#### `legator_kubeflow_submit_run`
**Description:** Submit a Kubeflow run/job manifest through policy gates.  
**Input:**
```json
{"name": "optional-name", "kind": "optional-kind", "namespace": "optional", "manifest": {...}}
```
Requires `LEGATOR_KUBEFLOW_ACTIONS_ENABLED=true`.

#### `legator_kubeflow_cancel_run`
**Description:** Cancel a Kubeflow run/job through policy gates.  
**Input:**
```json
{"name": "training-run-1", "kind": "runs.kubeflow.org", "namespace": "kubeflow"}
```

---

## Resources

Resources are read-only data endpoints accessible via the MCP resource protocol. They return `application/json` content.

---

### `legator://fleet/summary`

**Name:** Fleet Summary  
**Description:** Fleet-wide status counts, tags, and pending approval totals.

**Response:**
```json
{
  "total_probes": 15,
  "by_status": {"online": 12, "offline": 3},
  "tags": {"web": 5, "db": 3, "prod": 8},
  "pending_approvals": 0
}
```

---

### `legator://fleet/inventory`

**Name:** Fleet Inventory  
**Description:** Aggregated fleet inventory across all probes.

**Response:**
```json
{
  "probes": [...],
  "aggregates": {"total_cpus": 48, "total_ram_bytes": 206158430208}
}
```

---

### `legator://federation/inventory`

**Name:** Federation Inventory  
**Description:** Federated inventory across source adapters.

**URI query params (optional):** `?tag=prod&status=online&source=aws-east&tenant_id=acme`

**Permission:** PermFleetRead + federation access scope.

**Response:** Federation inventory payload (same as `GET /api/v1/federation/inventory`).

---

### `legator://federation/summary`

**Name:** Federation Summary  
**Description:** Federated source health and aggregate rollups.

**URI query params:** Same as federation/inventory.

**Permission:** PermFleetRead + federation access scope.

---

### `legator://jobs/list`

**Name:** Jobs List  
**Description:** Configured scheduled jobs.

**Response:**
```json
[{"id": "job-abc", "name": "nightly-backup", "schedule": "0 2 * * *", "enabled": true}]
```

---

### `legator://jobs/active-runs`

**Name:** Jobs Active Runs  
**Description:** Queued/pending/running job runs across all jobs.

**Response:**
```json
{
  "runs": [...],
  "count": 3
}
```

---

### `legator://grafana/status` *(conditional)*

**Name:** Grafana Status  
**Description:** Read-only Grafana adapter status summary.  
Available when `LEGATOR_GRAFANA_ENABLED=true`. **Permission:** PermFleetRead.

---

### `legator://grafana/snapshot` *(conditional)*

**Name:** Grafana Snapshot  
**Description:** Read-only Grafana capacity snapshot.  
Available when `LEGATOR_GRAFANA_ENABLED=true`. **Permission:** PermFleetRead.

---

### `legator://grafana/capacity-policy` *(conditional)*

**Name:** Grafana Capacity Policy  
**Description:** Grafana-derived capacity signals and policy rationale projection.  
Available when `LEGATOR_GRAFANA_ENABLED=true`. **Permission:** PermFleetRead.

---

## Error Handling

Tool errors are returned as MCP error responses with a human-readable message. Common errors:

| Message | Cause |
|---|---|
| `fleet store unavailable` | Control plane started without fleet subsystem |
| `probe not found: prb-xxx` | Invalid probe ID |
| `command transport unavailable` | WebSocket hub not running |
| `approval service unavailable` | Approval queue not configured |
| `jobs store unavailable` | Jobs scheduler not running |
| `federation store unavailable` | Federation not configured |
| `grafana adapter unavailable` | `LEGATOR_GRAFANA_ENABLED=false` |
| `kubeflow adapter unavailable` | `LEGATOR_KUBEFLOW_ENABLED=false` |
| `probe_id is required` | Missing required input field |

---

## Compatibility

See [docs/api-mcp-compatibility.md](api-mcp-compatibility.md) for the versioning and deprecation policy.
