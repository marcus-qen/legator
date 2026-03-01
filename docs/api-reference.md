# API Reference

**Version:** beta.1  
**Base URL:** `http(s)://<control-plane-host>:<port>`  
**Auth header:** `Authorization: Bearer lgk_<64hex>` (API key) or session cookie from `/login`

All `/api/v1/*` endpoints return `application/json`. POST/PUT/PATCH bodies must be JSON (max 1 MB). Errors follow:

```json
{"error": "not_found", "message": "probe not found"}
```

---

## System

### GET /healthz
**Permission:** None  
**Response:** `200 OK` `text/plain`
```
ok
```

### GET /version
**Permission:** None  
**Response:** `200 OK`
```json
{"version": "1.0.0-beta.1", "commit": "abc123", "date": "2026-03-01"}
```

---

## Authentication

### GET /login
Renders the HTML login page. Optional OIDC button if configured.

### POST /login
Form submission (`application/x-www-form-urlencoded`). Sets a session cookie on success.

**Form fields:** `username`, `password`  
**Response:** Redirect to `/` on success, re-rendered login page on failure.

### POST /logout
Invalidates the current session cookie.

**Response:** `200 OK` or redirect.

### GET /auth/oidc/login
Initiates OIDC authorization code + PKCE flow. Redirects to identity provider.  
Only available when OIDC is configured.

### GET /auth/oidc/callback
OIDC callback. Validates state, nonce, and ID token. Creates a Legator session.  
Only available when OIDC is configured.

### GET /api/v1/me
**Permission:** Authenticated (any)  
**Response:** `200 OK`
```json
{"username": "alice", "role": "operator", "permissions": ["fleet:read", "fleet:write"]}
```

---

## Admin

### GET /api/v1/users
**Permission:** PermAdmin  
**Response:** `200 OK`
```json
{"users": [{"id": "u-abc", "username": "alice", "role": "operator", "display_name": "Alice"}], "total": 1}
```

### POST /api/v1/users
**Permission:** PermAdmin  
**Request body:**
```json
{"username": "alice", "display_name": "Alice", "password": "secure123", "role": "operator"}
```
`role` must be one of: `admin`, `operator`, `viewer`  
**Response:** `201 Created`
```json
{"id": "u-abc", "username": "alice", "role": "operator", "display_name": "Alice"}
```

### DELETE /api/v1/users/{id}
**Permission:** PermAdmin  
**Response:** `200 OK`
```json
{"status": "deleted"}
```

### GET /api/v1/auth/keys
**Permission:** PermAdmin  
**Response:** `200 OK`
```json
{"keys": [{"id": "k-abc", "name": "ci-runner", "permissions": ["fleet:read"], "created_at": "2026-01-01T00:00:00Z"}]}
```

### POST /api/v1/auth/keys
**Permission:** PermAdmin  
**Request body:**
```json
{"name": "ci-runner", "permissions": ["fleet:read", "fleet:write"]}
```
**Response:** `201 Created`
```json
{"id": "k-abc", "name": "ci-runner", "key": "lgk_<64hex>", "permissions": ["fleet:read"]}
```
> The `key` field is only returned on creation. Store it securely.

### DELETE /api/v1/auth/keys/{id}
**Permission:** PermAdmin  
**Response:** `200 OK` or `404 Not Found`

---

## Registration Tokens

### POST /api/v1/tokens
**Permission:** FleetWrite  
**Query params:** `multi_use=true` (default false), `no_expiry=true` (default false)  
**Response:** `200 OK`
```json
{
  "token": "abc123...",
  "expires": "2026-03-02T00:00:00Z",
  "multi_use": false,
  "install_command": "curl -sSL https://cp.example.com/install.sh | sudo bash -s -- --server https://cp.example.com --token abc123..."
}
```

### GET /api/v1/tokens
**Permission:** PermAdmin  
**Response:** `200 OK`
```json
{"tokens": [...], "count": 3, "total": 10}
```

### POST /api/v1/register
**Permission:** None (token-authenticated)  
**Request body:**
```json
{
  "token": "abc123...",
  "hostname": "web-01",
  "os": "linux",
  "arch": "amd64",
  "version": "1.0.0",
  "tags": ["web", "prod"]
}
```
**Response:** `201 Created`
```json
{"probe_id": "prb-a1b2c3d4", "api_key": "lgk_<64hex>", "policy_id": "default-observe"}
```

---

## Fleet

### GET /api/v1/probes
**Permission:** FleetRead  
**Response:** `200 OK` — array of probe state objects
```json
[
  {
    "id": "prb-a1b2c3d4",
    "hostname": "web-01",
    "status": "online",
    "os": "linux",
    "arch": "amd64",
    "version": "1.0.0",
    "tags": ["web", "prod"],
    "policy_level": "observe",
    "last_seen": "2026-03-01T23:00:00Z",
    "registered": "2026-01-01T00:00:00Z"
  }
]
```

### GET /api/v1/probes/{id}
**Permission:** FleetRead  
**Response:** `200 OK` — single probe state object (same shape as above)  
`404 Not Found` if probe not registered.

### GET /api/v1/probes/{id}/health
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{"score": 92, "status": "healthy", "warnings": []}
```

### DELETE /api/v1/probes/{id}
**Permission:** FleetWrite  
Disconnects and deregisters the probe. Emits audit event.  
**Response:** `200 OK`
```json
{"deleted": "prb-a1b2c3d4"}
```

### GET /api/v1/fleet/summary
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{
  "counts": {"online": 12, "offline": 3, "degraded": 1},
  "connected": 12,
  "pending_approvals": 2,
  "reliability": { "overall": {...}, "control_plane": {...}, "probe_fleet": {...} }
}
```

### GET /api/v1/fleet/inventory
**Permission:** FleetRead  
**Query params:** `tag=<tag>`, `status=online|offline|degraded`  
**Response:** `200 OK`
```json
{
  "probes": [...],
  "aggregates": {"total_cpus": 48, "total_ram_bytes": 206158430208}
}
```

### GET /api/v1/fleet/tags
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{"tags": {"web": 5, "db": 3, "prod": 8}}
```

### GET /api/v1/fleet/by-tag/{tag}
**Permission:** FleetRead  
**Response:** `200 OK` — array of probe state objects matching the tag.

### POST /api/v1/fleet/by-tag/{tag}/command
**Permission:** FleetWrite (PermCommandExec)  
Dispatches a command to all probes matching the tag.  
**Request body:**
```json
{"command": "systemctl status nginx", "request_id": "optional-override"}
```
**Response:** `200 OK`
```json
{
  "tag": "web",
  "total": 5,
  "results": [
    {"probe_id": "prb-a1b2c3d4", "status": "dispatched", "request_id": "grp-a1b2c3d4-12345"},
    {"probe_id": "prb-b2c3d4e5", "status": "error", "error": "probe offline"}
  ]
}
```

### POST /api/v1/fleet/cleanup
**Permission:** FleetWrite  
Removes stale offline probes. Default threshold: 1 hour.  
**Query params:** `older_than=2h` (Go duration string)  
**Response:** `200 OK`
```json
{"removed": ["prb-old1", "prb-old2"], "count": 2}
```

### GET /api/v1/federation/inventory
**Permission:** FleetRead  
**Query params:** `tag`, `status`, `source`, `cluster`, `site`, `search`, `tenant_id` (or `tenant`), `org_id` (or `org`), `scope_id` (or `scope`)  
**Response:** `200 OK`
```json
{
  "sources": [...],
  "probes": [...],
  "consistency": {"freshness": 0.98, "completeness": 1.0, "partial_results": false}
}
```

### GET /api/v1/federation/summary
**Permission:** FleetRead  
Same query params as federation/inventory.  
**Response:** `200 OK`
```json
{
  "sources": [...],
  "aggregates": {"total_probes": 150, "online": 142},
  "consistency": {...}
}
```

---

## Probes

### POST /api/v1/probes/{id}/command
**Permission:** FleetWrite (PermCommandExec)  
**Query params:** `wait=true` (synchronous), `stream=true` (SSE stream)  
**Request body:**
```json
{"command": "df -h", "request_id": "req-abc123"}
```
**Response (immediate dispatch):** `200 OK`
```json
{"status": "dispatched", "request_id": "req-abc123"}
```
**Response (pending approval):** `202 Accepted`
```json
{
  "status": "pending_approval",
  "approval_id": "apr-xyz",
  "risk_level": "elevated",
  "expires_at": "2026-03-01T23:05:00Z",
  "policy_decision": "queue",
  "policy_rationale": {...},
  "message": "Command requires human approval."
}
```
**Response (capacity denied):** `429 Too Many Requests`
```json
{
  "status": "denied",
  "policy_decision": "deny",
  "risk_level": "destructive",
  "policy_rationale": {...},
  "message": "Command denied by capacity policy."
}
```

### POST /api/v1/probes/{id}/rotate-key
**Permission:** FleetWrite  
Generates a new API key for the probe and pushes it over the WebSocket connection.  
**Response:** `200 OK`
```json
{"status": "rotated", "probe_id": "prb-a1b2c3d4", "new_key": "lgk_<64hex>"}
```

### POST /api/v1/probes/{id}/update
**Permission:** FleetWrite  
Dispatches a self-update payload to the probe.  
**Request body:**
```json
{"url": "https://cp.example.com/download/probe-1.0.1-linux-amd64", "version": "1.0.1", "sha256": "abc..."}
```
**Response:** `200 OK`
```json
{"status": "dispatched", "version": "1.0.1"}
```

### PUT /api/v1/probes/{id}/tags
**Permission:** FleetWrite  
**Request body:**
```json
{"tags": ["web", "prod", "region-eu"]}
```
**Response:** `200 OK`
```json
{"probe_id": "prb-a1b2c3d4", "tags": ["web", "prod", "region-eu"]}
```

### POST /api/v1/probes/{id}/apply-policy/{policyId}
**Permission:** FleetWrite  
Applies a named policy template to the probe. Pushes update over WebSocket if online.  
**Response:** `200 OK`
```json
{"status": "applied", "probe_id": "prb-a1b2c3d4", "policy_id": "strict-observe", "level": "observe"}
```
If probe offline:
```json
{"status": "applied_locally", "note": "probe offline, policy saved but not pushed"}
```

### POST /api/v1/probes/{id}/task
**Permission:** FleetWrite (PermCommandExec)  
Runs an LLM-orchestrated task against the probe.  
**Request body:**
```json
{"task": "Check if disk usage is above 80% and restart nginx if memory is below 20%"}
```
**Response:** `200 OK` — task result with LLM reasoning and commands executed.

---

## Reliability

### GET /api/v1/reliability/scorecard
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{
  "overall": {"score": 94.2, "status": "healthy"},
  "control_plane": {
    "score": 98.0,
    "status": "healthy",
    "slo": {"target": 99.0, "warning": 95.0, "critical": 90.0, "comparator": ">=", "window": "15m"}
  },
  "probe_fleet": {
    "score": 90.4,
    "status": "warning",
    "sli": {"value": 90.4, "unit": "%", "sample_size": 15, "rationale": "2 probes offline"}
  }
}
```

### GET /api/v1/reliability/drills
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{"drills": [{"name": "chaos-offline", "description": "...", "last_run": "..."}]}
```

### POST /api/v1/reliability/drills/{name}/run
**Permission:** FleetWrite  
**Response:** `200 OK`
```json
{"drill": "chaos-offline", "status": "started", "run_id": "..."}
```

### GET /api/v1/reliability/drills/history
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{"history": [...]}
```

### POST /api/v1/reliability/incidents
**Permission:** FleetWrite  
**Request body:**
```json
{
  "title": "DB cluster degraded",
  "severity": "P2",
  "affected_probes": ["prb-a1b2c3d4", "prb-b2c3d4e5"],
  "description": "Optional details"
}
```
**Response:** `201 Created`
```json
{"incident": {"id": "inc-xyz", "title": "DB cluster degraded", "severity": "P2", "status": "open", "created_at": "..."}}
```

### GET /api/v1/reliability/incidents
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{"incidents": [...], "total": 5}
```

### GET /api/v1/reliability/incidents/{id}
**Permission:** FleetRead  
**Response:** `200 OK` — single incident object.

### PATCH /api/v1/reliability/incidents/{id}
**Permission:** FleetWrite  
**Request body:** Partial incident fields (`title`, `severity`, `status`, `resolved_at`, etc.)  
**Response:** `200 OK` — updated incident.

### POST /api/v1/reliability/incidents/{id}/timeline
**Permission:** FleetWrite  
**Request body:**
```json
{"note": "Identified root cause: OOM killer on db-01", "actor": "alice"}
```
**Response:** `200 OK` — updated incident with timeline entry.

### DELETE /api/v1/reliability/incidents/{id}
**Permission:** FleetWrite  
**Response:** `204 No Content`

### GET /api/v1/reliability/incidents/{id}/export
**Permission:** FleetRead  
**Response:** `200 OK` JSON export of the incident with full timeline.

---

## Alerts

### GET /api/v1/alerts
**Permission:** FleetRead  
**Response:** `200 OK`
```json
{"rules": [...]}
```

### POST /api/v1/alerts
**Permission:** FleetWrite  
**Request body:**
```json
{
  "name": "High offline rate",
  "condition": "offline_ratio > 0.2",
  "severity": "warning",
  "probe_ids": [],
  "tags": ["prod"]
}
```
**Response:** `201 Created` — new alert rule.

### GET /api/v1/alerts/active
**Permission:** FleetRead  
**Response:** `200 OK` — currently firing alerts.

### GET /api/v1/alerts/{id}
**Permission:** FleetRead  
**Response:** `200 OK` — single rule.

### PUT /api/v1/alerts/{id}
**Permission:** FleetWrite  
**Request body:** Full replacement of alert rule fields.  
**Response:** `200 OK`

### DELETE /api/v1/alerts/{id}
**Permission:** FleetWrite  
**Response:** `204 No Content`

### GET /api/v1/alerts/{id}/history
**Permission:** FleetRead  
**Response:** `200 OK` — past firing events for this rule.

### GET /api/v1/alerts/routing/policies
**Permission:** FleetRead  
**Response:** `200 OK` — list of routing policies.

### POST /api/v1/alerts/routing/policies
**Permission:** FleetWrite  
**Request body:**
```json
{
  "name": "pagerduty-p1",
  "matchers": [{"key": "severity", "value": "P1"}],
  "receivers": ["pagerduty"]
}
```
**Response:** `201 Created`

### POST /api/v1/alerts/routing/resolve
**Permission:** FleetRead  
Tests which routing policy an alert would match.  
**Request body:** Alert label set.  
**Response:** `200 OK` — matched policy.

### GET /api/v1/alerts/routing/policies/{id}
**Permission:** FleetRead

### PUT /api/v1/alerts/routing/policies/{id}
**Permission:** FleetWrite

### DELETE /api/v1/alerts/routing/policies/{id}
**Permission:** FleetWrite

### GET /api/v1/alerts/escalation/policies
**Permission:** FleetRead

### POST /api/v1/alerts/escalation/policies
**Permission:** FleetWrite

### GET /api/v1/alerts/escalation/policies/{id}
**Permission:** FleetRead

### PUT /api/v1/alerts/escalation/policies/{id}
**Permission:** FleetWrite

### DELETE /api/v1/alerts/escalation/policies/{id}
**Permission:** FleetWrite

---

## Policies

### GET /api/v1/policies
**Permission:** FleetRead  
**Response:** `200 OK` — array of policy templates.

### GET /api/v1/policies/{id}
**Permission:** FleetRead  
**Response:** `200 OK` — single policy template.

### POST /api/v1/policies
**Permission:** FleetWrite  
**Request body:**
```json
{
  "name": "strict-observe",
  "description": "Read-only with no process manipulation",
  "level": "observe",
  "allowed": ["df", "du", "ps", "top", "netstat"],
  "blocked": ["rm", "kill", "shutdown"],
  "paths": ["/var/log", "/etc"]
}
```
`level` is one of: `observe`, `diagnose`, `remediate`  
**Response:** `201 Created`

### DELETE /api/v1/policies/{id}
**Permission:** FleetWrite  
**Response:** `204 No Content`

---

## Chat

### GET /api/v1/probes/{id}/chat
**Permission:** FleetRead  
Returns chat history for the probe.  
**Response:** `200 OK`
```json
{"messages": [{"role": "user", "content": "...", "timestamp": "..."}]}
```

### POST /api/v1/probes/{id}/chat
**Permission:** FleetRead  
Sends a chat message and waits for LLM + probe response.  
**Request body:**
```json
{"message": "How much disk space is left on /var?"}
```
**Response:** `200 OK` — assistant reply.

### DELETE /api/v1/probes/{id}/chat
**Permission:** FleetRead  
Clears the chat history for the probe.  
**Response:** `200 OK`

### GET /ws/chat
**Permission:** FleetRead  
WebSocket endpoint for real-time probe chat. Query param: `probe_id=<id>`.

### GET /api/v1/fleet/chat
**Permission:** FleetRead  
Fleet-level chat history.

### POST /api/v1/fleet/chat
**Permission:** FleetRead  
Fleet-level chat (routes to LLM with fleet context).

### GET /ws/fleet-chat
**Permission:** FleetRead  
WebSocket for fleet chat.

---

## Commands

### GET /api/v1/commands/pending
**Permission:** PermCommandExec  
**Response:** `200 OK`
```json
{"pending": [...], "in_flight": 3}
```

### GET /api/v1/commands/{requestId}/stream
**Permission:** PermCommandExec  
SSE stream of output chunks for a running command.  
**Response:** `text/event-stream`
```
data: {"chunk": "Filesystem  ...", "final": false}

data: {"chunk": "", "final": true}
```

---

## Approvals

### GET /api/v1/approvals
**Permission:** PermApprovalRead  
**Query params:** `status=pending`, `limit=50`  
**Response:** `200 OK`
```json
{
  "approvals": [
    {
      "id": "apr-xyz",
      "probe_id": "prb-a1b2c3d4",
      "command": "rm -rf /tmp/old",
      "risk_level": "elevated",
      "expires_at": "...",
      "policy_decision": "queue",
      "policy_rationale": {"summary": "...", "drove_outcome": true}
    }
  ],
  "pending_count": 1
}
```

### GET /api/v1/approvals/{id}
**Permission:** PermApprovalRead  
**Response:** `200 OK` — single approval request.

### POST /api/v1/approvals/{id}/decide
**Permission:** PermApprovalWrite  
**Request body:**
```json
{"decision": "approved", "decided_by": "alice"}
```
`decision` is `approved` or `denied`.  
**Response:** `200 OK`
```json
{"status": "dispatched", "request_id": "req-abc123"}
```

---

## Discovery

### POST /api/v1/discovery/scan
**Permission:** FleetWrite  
Triggers a network discovery scan.  
**Request body:**
```json
{"cidr": "192.168.1.0/24", "ports": [22, 80, 443], "timeout": "30s"}
```
**Response:** `202 Accepted`
```json
{"run_id": "disc-abc", "status": "running"}
```

### GET /api/v1/discovery/runs
**Permission:** FleetRead  
**Response:** `200 OK` — list of discovery runs.

### GET /api/v1/discovery/runs/{id}
**Permission:** FleetRead  
**Response:** `200 OK` — single run with results.

### POST /api/v1/discovery/install-token
**Permission:** FleetWrite  
Generates a registration token linked to a discovered host.  
**Request body:**
```json
{"host": "192.168.1.50", "run_id": "disc-abc"}
```
**Response:** `200 OK` — token object with install_command.

---

## Automation Packs

### GET /api/v1/automation-packs
**Permission:** FleetRead  
**Response:** `200 OK` — list of automation pack definitions.

### POST /api/v1/automation-packs
**Permission:** FleetWrite  
**Request body:** Automation pack definition JSON.  
**Response:** `201 Created`

### GET /api/v1/automation-packs/{id}
**Permission:** FleetRead  
**Query params:** `version=x.y.z` (optional)  
**Response:** `200 OK` — single definition.

### POST /api/v1/automation-packs/dry-run
**Permission:** FleetWrite  
Validates a pack definition without executing.  
**Response:** `200 OK` — validation result.

### POST /api/v1/automation-packs/{id}/executions
**Permission:** FleetWrite  
Starts an execution of the pack.  
**Response:** `201 Created`
```json
{"execution_id": "exec-abc", "status": "running"}
```

### GET /api/v1/automation-packs/executions/{executionID}
**Permission:** FleetRead  
**Response:** `200 OK` — execution state.

### GET /api/v1/automation-packs/executions/{executionID}/timeline
**Permission:** FleetRead  
**Response:** `200 OK` — ordered timeline of execution steps.

### GET /api/v1/automation-packs/executions/{executionID}/artifacts
**Permission:** FleetRead  
**Response:** `200 OK` — list of output artifacts.

---

## Federation

### GET /api/v1/federation/inventory
See **Fleet → GET /api/v1/federation/inventory** above.

### GET /api/v1/federation/summary
See **Fleet → GET /api/v1/federation/summary** above.

---

## Cloud Connectors

### GET /api/v1/cloud/connectors
**Permission:** FleetRead  
**Response:** `200 OK` — list of cloud connector configurations.

### POST /api/v1/cloud/connectors
**Permission:** FleetWrite  
**Request body:**
```json
{"name": "aws-prod", "provider": "aws", "region": "eu-west-1", "credentials": {...}}
```
**Response:** `201 Created`

### PUT /api/v1/cloud/connectors/{id}
**Permission:** FleetWrite  
Full update of connector configuration.  
**Response:** `200 OK`

### DELETE /api/v1/cloud/connectors/{id}
**Permission:** FleetWrite  
**Response:** `204 No Content`

### POST /api/v1/cloud/connectors/{id}/scan
**Permission:** FleetWrite  
Triggers an agentless asset scan for the connector.  
**Response:** `202 Accepted`

### GET /api/v1/cloud/assets
**Permission:** FleetRead  
**Response:** `200 OK` — list of discovered cloud assets.

---

## Model Dock

### GET /api/v1/model-profiles
**Permission:** FleetRead  
**Response:** `200 OK` — list of LLM model profiles.

### POST /api/v1/model-profiles
**Permission:** FleetWrite  
**Request body:**
```json
{
  "name": "gpt-4o-prod",
  "provider": "openai",
  "model": "gpt-4o",
  "api_key_env": "OPENAI_API_KEY",
  "max_tokens": 4096
}
```
**Response:** `201 Created`

### PUT /api/v1/model-profiles/{id}
**Permission:** FleetWrite  
**Response:** `200 OK`

### DELETE /api/v1/model-profiles/{id}
**Permission:** FleetWrite  
**Response:** `204 No Content`

### POST /api/v1/model-profiles/{id}/activate
**Permission:** FleetWrite  
Sets this profile as the active LLM for the control plane.  
**Response:** `200 OK`

### GET /api/v1/model-profiles/active
**Permission:** FleetRead  
**Response:** `200 OK` — currently active profile.

### GET /api/v1/model-usage
**Permission:** FleetRead  
**Response:** `200 OK` — token usage statistics per profile.

---

## Network Devices

### GET /api/v1/network/devices
**Permission:** FleetRead  
**Response:** `200 OK` — list of managed network devices.

### POST /api/v1/network/devices
**Permission:** FleetWrite  
**Request body:**
```json
{"name": "core-switch-01", "host": "10.0.0.1", "protocol": "snmp", "community": "public"}
```
**Response:** `201 Created`

### GET /api/v1/network/devices/{id}
**Permission:** FleetRead  
**Response:** `200 OK` — single device.

### PUT /api/v1/network/devices/{id}
**Permission:** FleetWrite  
**Response:** `200 OK`

### DELETE /api/v1/network/devices/{id}
**Permission:** FleetWrite  
**Response:** `204 No Content`

### POST /api/v1/network/devices/{id}/test
**Permission:** FleetWrite  
Connectivity test for the device.  
**Response:** `200 OK`
```json
{"reachable": true, "latency_ms": 2}
```

### POST /api/v1/network/devices/{id}/inventory
**Permission:** FleetWrite  
Triggers an inventory poll (interfaces, routes, ARP table).  
**Response:** `202 Accepted`

---

## Jobs

### GET /api/v1/jobs
**Permission:** FleetRead  
**Response:** `200 OK` — list of scheduled jobs.

### POST /api/v1/jobs
**Permission:** FleetWrite  
**Request body:**
```json
{
  "name": "nightly-backup",
  "schedule": "0 2 * * *",
  "command": "rsync -av /data /backup",
  "probe_ids": ["prb-a1b2c3d4"],
  "retry_policy": {
    "max_attempts": 3,
    "initial_backoff": "10s",
    "multiplier": 2,
    "max_backoff": "5m"
  }
}
```
**Response:** `201 Created`

### GET /api/v1/jobs/runs
**Permission:** FleetRead  
All runs across all jobs. Supports status filter.  
**Response:** `200 OK`

### GET /api/v1/jobs/{id}
**Permission:** FleetRead  
**Response:** `200 OK`

### PUT /api/v1/jobs/{id}
**Permission:** FleetWrite  
**Response:** `200 OK`

### DELETE /api/v1/jobs/{id}
**Permission:** FleetWrite  
**Response:** `204 No Content`

### POST /api/v1/jobs/{id}/run
**Permission:** FleetWrite  
Manually triggers a job run.  
**Response:** `202 Accepted`
```json
{"run_id": "run-xyz", "status": "queued"}
```

### POST /api/v1/jobs/{id}/cancel
**Permission:** FleetWrite  
Cancels all active runs for a job.  
**Response:** `200 OK`

### GET /api/v1/jobs/{id}/runs
**Permission:** FleetRead  
**Response:** `200 OK` — run history for the job, including `execution_id`, `attempt`, `admission_decision`.

### POST /api/v1/jobs/{id}/runs/{runId}/cancel
**Permission:** FleetWrite  
**Response:** `200 OK`

### POST /api/v1/jobs/{id}/runs/{runId}/retry
**Permission:** FleetWrite  
**Response:** `202 Accepted`

### POST /api/v1/jobs/{id}/enable
**Permission:** FleetWrite  
**Response:** `200 OK`

### POST /api/v1/jobs/{id}/disable
**Permission:** FleetWrite  
**Response:** `200 OK`

---

## Webhooks

### GET /api/v1/webhooks
**Permission:** PermWebhookManage  
**Response:** `200 OK` — list of webhook endpoints.

### GET /api/v1/webhooks/deliveries
**Permission:** PermWebhookManage  
**Response:** `200 OK` — recent delivery log.

### POST /api/v1/webhooks
**Permission:** PermWebhookManage  
**Request body:**
```json
{"url": "https://hooks.example.com/legator", "secret": "optional-hmac-secret", "events": ["probe.offline", "approval.request"]}
```
**Response:** `201 Created`

### GET /api/v1/webhooks/{id}
**Permission:** PermWebhookManage

### DELETE /api/v1/webhooks/{id}
**Permission:** PermWebhookManage

### POST /api/v1/webhooks/{id}/test
**Permission:** PermWebhookManage  
Sends a test payload.  
**Response:** `200 OK`

---

## Audit

### GET /api/v1/audit
**Permission:** PermAuditRead  
**Query params:** `probe_id`, `type`, `since` (RFC3339), `limit` (default 50), `cursor` (for pagination)  
**Response:** `200 OK`
```json
{
  "events": [
    {
      "id": "evt-abc",
      "timestamp": "2026-03-01T23:00:00Z",
      "type": "command.sent",
      "probe_id": "prb-a1b2c3d4",
      "actor": "alice",
      "summary": "Command dispatched: df -h",
      "detail": {"command": "df -h", "request_id": "req-abc"}
    }
  ],
  "total": 1543,
  "next_cursor": "evt-prev",
  "has_more": true
}
```

### GET /api/v1/audit/export
**Permission:** PermAuditRead  
JSONL (newline-delimited JSON) export. Supports `since`, `until`, `probe_id`, `type`.  
**Response:** `application/x-ndjson` file download (`legator-audit-YYYYMMDD.jsonl`).

### GET /api/v1/audit/export/csv
**Permission:** PermAuditRead  
CSV export with same filters.  
**Response:** `text/csv` file download.

### DELETE /api/v1/audit/purge
**Permission:** PermAdmin  
**Query params:** `older_than=30d` (required, Go duration)  
**Response:** `200 OK`
```json
{"deleted": 1243}
```

---

## Events (SSE)

### GET /api/v1/events
**Permission:** FleetRead  
Server-Sent Events stream of platform events.  
**Response:** `text/event-stream`
```
: connected

event: probe.offline
data: {"probe_id": "prb-a1b2c3d4", "hostname": "web-01", "timestamp": "..."}

event: job.run.failed
data: {"job_id": "job-abc", "run_id": "run-xyz", "execution_id": "exec-123", "probe_id": "prb-a1b2c3d4"}
```

Event types include: `probe.online`, `probe.offline`, `command.dispatched`, `approval.request`, `job.created`, `job.run.queued`, `job.run.started`, `job.run.succeeded`, `job.run.failed`, `job.run.canceled`, `job.run.denied`, `job.run.retry_scheduled`, and more.

---

## MCP

### GET /mcp  
### POST /mcp

SSE-based Model Context Protocol endpoint. See [docs/mcp-tools.md](mcp-tools.md) for full tool and resource documentation.

**Content-Type:** `text/event-stream` (SSE transport)  
**Auth:** Same `Authorization: Bearer` header as REST endpoints.

---

## Metrics

### GET /api/v1/metrics
**Permission:** FleetRead  
**Response:** `200 OK` — Prometheus-compatible metrics snapshot.
```json
{
  "probes_total": 15,
  "probes_online": 12,
  "probes_offline": 3,
  "commands_dispatched_total": 4521,
  "approvals_pending": 2,
  "audit_events_total": 15430,
  "webhook_deliveries_total": 210,
  "webhook_delivery_failures_total": 3
}
```

---

## Adapters (Optional)

### Kubeflow

#### GET /api/v1/kubeflow/status
**Permission:** FleetRead — Requires `LEGATOR_KUBEFLOW_ENABLED=true`

#### GET /api/v1/kubeflow/inventory
**Permission:** FleetRead

#### GET /api/v1/kubeflow/runs/{name}/status
**Permission:** FleetRead

#### POST /api/v1/kubeflow/actions/refresh
**Permission:** FleetWrite — Requires `LEGATOR_KUBEFLOW_ACTIONS_ENABLED=true`

#### POST /api/v1/kubeflow/runs/submit
**Permission:** FleetWrite — mutations disabled by default

#### POST /api/v1/kubeflow/runs/{name}/cancel
**Permission:** FleetWrite — mutations disabled by default

### Grafana

#### GET /api/v1/grafana/status
**Permission:** FleetRead — Requires `LEGATOR_GRAFANA_ENABLED=true`

#### GET /api/v1/grafana/snapshot
**Permission:** FleetRead — capacity snapshot with dashboard coverage, datasource health.

---

## WebSocket

### GET /ws/probe
Probe connection endpoint. Probes authenticate with their API key (`lgk_*`) in the initial handshake message. Not intended for human clients.

---

## Binary Downloads

### GET /install.sh
Returns the probe install shell script (plain text).

### GET /download/{filename}
Returns binary release artifact from `$LEGATOR_DATA_DIR/releases/`.
