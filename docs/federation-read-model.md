# Federation Read Model (Stage 3.7.4)

Legator exposes a **read-only federation inventory layer** that aggregates inventory snapshots across multiple sources (clusters/sites) with explicit source attribution, health rollups, and filter parity across API/MCP/UI surfaces.

Compatibility/deprecation policy for API + MCP identifiers: `docs/api-mcp-compatibility.md`.

## Scope

- Read-only federation inventory listing
- Read-only federation summary rollups
- Per-source attribution on every probe summary (`source.id`, `source.cluster`, `source.site`, `source.tenant_id`, `source.org_id`, `source.scope_id`)
- Unified federation filters across all read surfaces: `source`, `cluster`, `site`, `tag`, `status`, `search`, `tenant_id`, `org_id`, `scope_id`
- Scoped federation authorization boundaries (tenant/org/scope) across API + MCP surfaces
- Health rollups:
  - overall federation health (`healthy` / `degraded` / `unavailable` / `unknown`)
  - per-source status and warnings/errors
- Explicit failover + consistency guard semantics:
  - cached snapshot failover when a source is temporarily unavailable
  - stale snapshot degradation markers
  - partial-result completeness indicators at source and rollup levels
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
| `search` | Case-insensitive free-text search across source/probe fields (source id/name/kind/cluster/site/tenant/org/scope, probe id/hostname/status/os/arch/kernel/policy/tags) |
| `tenant_id` (`tenant`) | Filter by source tenant identifier |
| `org_id` (`org`) | Filter by source organization identifier |
| `scope_id` (`scope`) | Filter by source scope identifier |

## MCP parity

Federation read parity is available through MCP tools/resources with the same filter fields (`tag`, `status`, `source`, `cluster`, `site`, `search`, `tenant_id`, `org_id`, `scope_id`).

### MCP tools

- `legator_federation_inventory`
- `legator_federation_summary`

### MCP resources

- `legator://federation/inventory`
- `legator://federation/summary`

Resource URIs support query parameters, for example:

- `legator://federation/inventory?cluster=primary&tag=prod&tenant_id=acme&scope_id=prod&search=web`

## UI parity

The web UI now includes a dedicated Federation view at `/federation` that calls:

- `GET /api/v1/federation/inventory`
- `GET /api/v1/federation/summary`

with shared filter inputs (`source`, `cluster`, `site`, `tag`, `status`, `search`). Tenancy segmentation still applies based on caller scope grants.

## Response model

### `/api/v1/federation/inventory`

Returns:

- `probes[]` with `source` attribution and probe summary payload
- `sources[]` with per-source aggregates and source-level health context
  - each source now includes additive `consistency` metadata:
    - `freshness` (`fresh` / `stale` / `unknown`)
    - `completeness` (`complete` / `partial` / `unavailable`)
    - `degraded` (boolean)
    - `failover_mode` (`none` / `cached_snapshot` / `unavailable`)
    - `snapshot_age_seconds` (when snapshot timestamp is available)
- `aggregates` with cross-source totals and distributions (including `tag_distribution`, `tenant_distribution`, `org_distribution`, `scope_distribution`)
- `health` with overall + per-source status rollups
- additive top-level `consistency` rollup:
  - `freshness` (`fresh` / `mixed` / `stale` / `unknown`)
  - `completeness` (`complete` / `partial` / `unavailable` / `unknown`)
  - `partial_results`, `failover_active`, and source counters (`stale_sources`, `partial_sources`, `unavailable_sources`, `failover_sources`)

### `/api/v1/federation/summary`

Returns the same source/aggregate/health/consistency rollups without the full `probes[]` list.

## Scoped authorization model

Federation reads continue to require `fleet:read`. In Stage 3.7.3, optional additive scope grants can further constrain federation reads:

- `tenant:<tenant-id>`
- `org:<org-id>`
- `scope:<scope-id>`

Equivalent prefixed grants (`federation:tenant:*`, `federation:org:*`, `federation:scope:*`) are also accepted. When these grants are present:

- callers only receive sources/probes within permitted tenant/org/scope boundaries
- explicit unauthorized tenant/org/scope filter requests return `403` (`code: forbidden_scope`) on REST surfaces
- MCP tools/resources return scoped authorization errors

When no tenant/org/scope grants are configured, behavior remains backward-compatible and effectively single-tenant (`default` tenancy IDs when unset).

## Source status + failover semantics

- `healthy`: source read succeeded, snapshot is fresh, and no partial/warning guards are active.
- `degraded`:
  - source read succeeded but reported partial data and/or warnings, **or**
  - source snapshot is stale, **or**
  - source read failed but Legator served a cached snapshot (`failover_mode: cached_snapshot`).
- `unavailable`: source read failed and no cached snapshot is available for the requested filter set (`failover_mode: unavailable`).
- `unknown`: no sources matched the query filters.

## Consistency guard behavior

- A successful source read updates the snapshot cache used for outage failover.
- During source outage, Legator attempts cached-snapshot failover for the same source + (`tag`, `status`) filter tuple.
- Cached failover responses remain read-only and explicitly marked as degraded/partial with `failover_mode: cached_snapshot` and source error context.
- Snapshot freshness is guarded by a stale threshold (default: 5 minutes). Stale snapshots are marked degraded with `freshness: stale` and a warning.
- Rollup consistency signals (`partial_results`, `failover_active`, freshness/completeness enums) are propagated consistently across REST + MCP + UI surfaces.

## Current wiring

By default, the control plane registers one local federation source (`local`) backed by the existing fleet inventory store. If tenant/org/scope IDs are not provided by an adapter, Legator normalizes them to `default` for backward compatibility. The federation store supports registering additional adapters for remote clusters/sites/tenancies without changing existing fleet write paths.
