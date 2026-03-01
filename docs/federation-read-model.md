# Federation Read Model (Stage 3.7.1)

Legator now exposes a **read-only federation inventory layer** that aggregates inventory snapshots across multiple sources (clusters/sites) with explicit source attribution and health rollups.

Compatibility/deprecation policy for API + MCP identifiers: `docs/api-mcp-compatibility.md`.

## Scope

- Read-only federation inventory listing
- Read-only federation summary rollups
- Per-source attribution on every probe summary (`source.id`, `source.cluster`, `source.site`, etc.)
- Health rollups:
  - overall federation health (`healthy` / `degraded` / `unavailable` / `unknown`)
  - per-source status and warnings/errors
- **No write/mutation operations**

## API endpoints

Both endpoints require `fleet:read` and are additive to existing fleet APIs.

- `GET /api/v1/federation/inventory`
- `GET /api/v1/federation/summary`

### Query parameters

| Param | Description |
|---|---|
| `tag` | Filter probes by tag (forwarded to each source adapter) |
| `status` | Filter probes by probe status (e.g. `online`) |
| `source` | Filter by source ID/name |
| `cluster` | Filter by source cluster |
| `site` | Filter by source site |

## Response model

### `/api/v1/federation/inventory`

Returns:

- `probes[]` with `source` attribution and probe summary payload
- `sources[]` with per-source aggregates and source-level health context
- `aggregates` with cross-source totals and distributions
- `health` with overall + per-source status rollups

### `/api/v1/federation/summary`

Returns the same source/aggregate/health rollups without the full `probes[]` list.

## Source status semantics

- `healthy`: source read succeeded without partial flags/warnings
- `degraded`: source read succeeded but reported partial data and/or warnings
- `unavailable`: source read failed
- `unknown`: no sources matched the query filters

## Current wiring

By default, the control plane registers one local federation source (`local`) backed by the existing fleet inventory store. The federation store supports registering additional adapters for remote clusters/sites without changing existing fleet write paths.
