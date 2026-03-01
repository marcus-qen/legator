# Grafana Adapter (Stage 2.1)

Legator exposes a **read-only Grafana capacity adapter** for policy-safe availability snapshots.

## Scope

- Read-only Grafana status endpoint
- Read-only capacity snapshot endpoint
- Service health + datasource + dashboard/panel/query coverage signals
- Safe-by-default: adapter disabled unless explicitly enabled
- **No write/mutation operations**

## Configuration

Set these environment variables on the control plane:

| Variable | Default | Purpose |
|---|---|---|
| `LEGATOR_GRAFANA_ENABLED` | `false` | Enable Grafana adapter endpoints |
| `LEGATOR_GRAFANA_BASE_URL` | — | Grafana base URL (e.g. `https://grafana.example.com`) |
| `LEGATOR_GRAFANA_API_TOKEN` | — | Optional Grafana API token (Bearer) |
| `LEGATOR_GRAFANA_TIMEOUT` | `10s` | Timeout per Grafana API call |
| `LEGATOR_GRAFANA_DASHBOARD_LIMIT` | `10` | Max dashboards scanned per snapshot (capped at 100) |
| `LEGATOR_GRAFANA_TLS_SKIP_VERIFY` | `false` | Skip TLS verification for self-signed lab certs |
| `LEGATOR_GRAFANA_ORG_ID` | `0` | Optional Grafana organization ID header |

## API endpoints

Both endpoints require `fleet:read` and are read-only.

- `GET /api/v1/grafana/status`
- `GET /api/v1/grafana/snapshot`

If disabled, both return:

```json
{"code":"service_unavailable","error":"grafana adapter unavailable"}
```

## MCP parity (Stage 3.2)

When the Grafana adapter is enabled, the MCP surface now exposes read-only parity tools/resources.

### MCP tools

- `legator_grafana_status` → payload parity with `GET /api/v1/grafana/status`
- `legator_grafana_snapshot` → payload parity with `GET /api/v1/grafana/snapshot`
- `legator_grafana_capacity_policy` → policy-facing capacity view:
  - `capacity` (`source`, `availability`, coverage, datasource count, `partial`, warnings)
  - `policy_decision` (`allow` / `queue` / `deny`)
  - `policy_rationale` (same rationale contract used by command policy responses)

### MCP resources

- `legator://grafana/status`
- `legator://grafana/snapshot`
- `legator://grafana/capacity-policy`

### Permissions

Grafana MCP tools/resources require `fleet:read` (matching REST Grafana read routes).

## Snapshot fields (minimal practical signals)

- **Service health**: database status/version/commit + healthy bool
- **Datasource summary**: totals, default/read-only counts, type breakdown
- **Dashboard summary**: dashboards scanned, panel counts, query-backed panel counts, datasource coverage
- **Capacity indicators**:
  - `availability` (`ready` / `limited` / `insufficient` / `degraded`)
  - `dashboard_coverage`
  - `query_coverage`
  - `datasource_count`

These fields are additive and consumed by Stage 2.2 command policy evaluation.

## Stage 2.2 policy integration (allow / deny / queue)

Command dispatch now evaluates a capacity-aware policy decision before dispatching:

- `allow` — command proceeds (existing behaviour)
- `queue` — command is routed to the approval queue
- `deny` — command is rejected immediately when capacity risk is too high

Policy evaluation is **safe-by-default**:

- If Grafana is disabled or unavailable, Legator falls back to risk-only approval logic.
- Fallback is surfaced in machine-readable rationale (`fallback: true`).

### Additive API response fields

When a command is queued or denied, `POST /api/v1/probes/{id}/command` now includes:

- `policy_decision` (`allow` / `queue` / `deny`)
- `policy_rationale` object:
  - `policy` version id
  - `summary`
  - `fallback` bool
  - `thresholds`
  - `capacity` signal snapshot (when available)
  - `indicators[]` with `drove_outcome` to show which signals triggered the decision

## Stage 2.3 operator explainability panel

The approvals workflow now renders Stage 2.2 rationale fields directly in the operator UI (`/approvals`):

- Human-readable summary (`policy_rationale.summary`)
- Decision badge (`policy_decision`)
- Capacity indicators (availability, datasource count, coverage percentages)
- Indicator list highlighting which signals **drove** the final outcome
- Expandable machine-readable JSON rationale block for precise triage/export

### Approval payload continuity

Queued approvals now persist the same additive explainability fields already emitted by command dispatch responses:

- `policy_decision`
- `policy_rationale`

This is additive-only and does not change existing approval route contracts.

## Example

```bash
LEGATOR_GRAFANA_ENABLED=true \
LEGATOR_GRAFANA_BASE_URL=https://grafana.example.com \
LEGATOR_GRAFANA_API_TOKEN=<token> \
./bin/control-plane

curl -sf http://localhost:8080/api/v1/grafana/status | jq
curl -sf http://localhost:8080/api/v1/grafana/snapshot | jq
```
