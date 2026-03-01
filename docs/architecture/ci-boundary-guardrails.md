# CI Architecture Guardrails Contract (Stage 3.6.3)

This document defines the architecture boundary contract used to drive CI guardrails.

Machine-readable source of truth:

- `docs/contracts/architecture-boundaries.yaml`
- `docs/contracts/architecture-cross-boundary-imports.txt` (Stage 3.6.3 baseline lock)

## Scope

The contract defines:

1. Boundary zones and package coverage.
2. Allow/deny dependency policy between zones.
3. Ownership assignments for each boundary.
4. Enforcement model for CI architecture guardrails.

## Boundary zones

| Boundary ID | Purpose | Package examples |
|---|---|---|
| `core-domain` | Domain policy and orchestration (transport-agnostic business logic) | `internal/controlplane/core/...`, `approval`, `policy`, `jobs`, `fleet`, `audit` |
| `adapters-integrations` | External provider/integration adapters | `grafana`, `kubeflow`, `networkdevices`, `cloudconnectors`, `modeldock`, `llm` |
| `surfaces` | Public/operator surfaces (HTTP, MCP, UI) | `internal/controlplane/server/...`, `mcpserver`, `api`, `web/...` |
| `platform-runtime` | Runtime wiring and shared infrastructure concerns | `auth`, `config`, `session`, `events`, `metrics`, `websocket`, `internal/shared/...`, `internal/protocol/...` |
| `probe-runtime` | Probe-side runtime and host execution lifecycle | `cmd/probe/...`, `internal/probe/...` |

## Dependency policy (contract)

Default policy is **deny**. Explicit allow rules define approved edges.

### Key allow edges

- `surfaces -> core-domain`
- `surfaces -> adapters-integrations` (transitional; existing handlers still wire adapters directly)
- `surfaces -> platform-runtime`
- `core-domain -> adapters-integrations`
- `core-domain -> platform-runtime`
- `adapters-integrations -> platform-runtime`
- `adapters-integrations -> core-domain` (transitional; current `llm` package reads fleet/core projections)
- `platform-runtime -> core-domain`
- `platform-runtime -> adapters-integrations`
- `platform-runtime -> surfaces` (transitional; discovery runtime currently reuses API registration helpers)
- `probe-runtime -> platform-runtime`

### Key deny edges

- `core-domain -/-> surfaces`
- `adapters-integrations -/-> surfaces`
- `probe-runtime -/-> surfaces`
- `probe-runtime -/-> core-domain`

## Ownership map

| Boundary | Owner area |
|---|---|
| `core-domain` | Control Plane Core |
| `adapters-integrations` | Integrations and Connectors |
| `surfaces` | Product Surfaces |
| `platform-runtime` | Runtime Platform |
| `probe-runtime` | Probe Runtime |

Ownership details and key module patterns are stored in the YAML contract.

## CI model

### Stage 3.6.1

- Validate contract integrity in CI:
  - required boundary IDs exist
  - dependency rules are internally consistent
  - ownership assignments are complete and valid
  - package patterns resolve to existing paths

### Stage 3.6.2

- Parse `go list` package imports for repository packages.
- Classify packages into boundaries using `package_patterns` in the contract.
- Enforce cross-boundary edges:
  - fail on any explicit `dependency_policy.deny` edge
  - fail on undeclared cross-boundary edges (default deny)
- Emit deterministic, actionable violation messages containing:
  - source package + boundary
  - imported package + boundary
  - edge (`from->to`)
  - rule reference (`dependency_policy.deny[...]` or default-deny reference)

### Stage 3.6.3 (implemented)

- Lock the observed package-level cross-boundary imports to:
  - `docs/contracts/architecture-cross-boundary-imports.txt`
- CI fails when:
  - a new cross-boundary package import appears and is not in the baseline, or
  - a baseline entry becomes stale (import removed but baseline not updated)
- Drift output is deterministic and actionable, including explicit lists of
  new vs stale entries and an update command.

## Validation entrypoints

```bash
# Direct compatibility + boundary guard checks
go test ./internal/controlplane/compat -count=1

# Convenience make target for local lint gate
make architecture-guard
```

To intentionally refresh the Stage 3.6.3 baseline after reviewed architecture changes:

```bash
LEGATOR_UPDATE_ARCH_IMPORT_BASELINE=1 go test ./internal/controlplane/compat -run TestBoundaryContract_ImportGraphBaselineLock -count=1
```

CI wiring:

- Test job: `go test ./internal/controlplane/compat -count=1`
- Lint job gate: `make architecture-guard GO=go`
