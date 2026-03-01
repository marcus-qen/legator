# CI Architecture Guardrails Contract (Stage 3.6.4)

This document defines the architecture boundary contract used to drive CI and local fast-fail guardrails.

Machine-readable sources of truth:

- `docs/contracts/architecture-boundaries.yaml`
- `docs/contracts/architecture-cross-boundary-imports.txt` (Stage 3.6.3 baseline lock)
- `docs/contracts/architecture-boundary-exceptions.yaml` (Stage 3.6.4 exception registry)

## Scope

The contract defines:

1. Boundary zones and package coverage.
2. Allow/deny dependency policy between zones.
3. Ownership assignments for each boundary.
4. Enforcement model for CI architecture guardrails.
5. Exception declaration + escalation process for intentional transitional edges.

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
- `surfaces -> adapters-integrations` (transitional; tracked in exception registry)
- `surfaces -> platform-runtime`
- `core-domain -> adapters-integrations`
- `core-domain -> platform-runtime`
- `adapters-integrations -> platform-runtime`
- `adapters-integrations -> core-domain` (transitional; tracked in exception registry)
- `platform-runtime -> core-domain`
- `platform-runtime -> adapters-integrations`
- `platform-runtime -> surfaces` (transitional; tracked in exception registry)
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

## Fast-fail workflow (local + CI)

### Local preflight (recommended before push)

```bash
# Fast-fail architecture checks only
make architecture-guard GO=go

# Contributor preflight: architecture checks first, then full test suite
make preflight GO=go
```

What this runs:

- Contract integrity + dependency policy consistency
- Ownership assignment validation
- Import-graph deny/default-deny enforcement
- Cross-boundary baseline lock drift check
- Exception registry validation (required metadata + expiry + transitional edge coverage)

### CI fast-fail

CI runs a dedicated **preflight guardrails** job first (`make architecture-guard GO=go`).
All other jobs (`test`, `build`, `lint`, `e2e`) depend on this gate. If guardrails fail, the workflow stops early before expensive build/e2e work.

## Common violations and fixes

### 1) Deny-edge import violation

Example:

```text
[DENY] package=... (core-domain) imports=... (surfaces) edge=core-domain->surfaces
```

Fix:

- Remove the forbidden import.
- Move transport/UI logic out of core package.
- Introduce an interface/projection seam in core and wire it from surfaces/runtime.

### 2) Undeclared cross-boundary edge (default deny)

Example:

```text
[UNDECLARED] ... edge=surfaces->probe-runtime rule=dependency_policy.default_effect
```

Fix:

- Prefer refactor to stay within existing allowed edges.
- If the edge is truly required, add an explicit `dependency_policy.allow` rule with rationale and follow the exception process below.

### 3) Baseline drift

Example:

```text
architecture import baseline drift detected ...
- new cross-boundary imports ...
- stale baseline entries ...
```

Fix:

- If drift is unintentional: revert/fix imports.
- If intentional and reviewed: refresh baseline, then commit contract + baseline + changelog/release rationale together.

```bash
LEGATOR_UPDATE_ARCH_IMPORT_BASELINE=1 go test ./internal/controlplane/compat -run TestBoundaryContract_ImportGraphBaselineLock -count=1
```

### 4) Missing/expired exception metadata

Example:

```text
missing exception registry entries for transitional allow edges ...
```

or

```text
... is expired as of YYYY-MM-DD; remove the exception or extend with fresh reviewer sign-off
```

Fix:

- Add/update entry in `docs/contracts/architecture-boundary-exceptions.yaml`.
- Include fresh reviewer sign-off, rationale, tracking issue, and new expiry.
- If edge is no longer needed, remove allow rule + exception entry in the same PR.

## Exception + escalation process (intentional boundary exceptions)

Use this process when a boundary exception is intentional and time-bounded.

1. **Declare the edge in contract allow policy**
   - Update `docs/contracts/architecture-boundaries.yaml` (`dependency_policy.allow`).
   - Rationale must explicitly explain why refactor is deferred.

2. **Declare the exception in registry**
   - Add an entry to `docs/contracts/architecture-boundary-exceptions.yaml`.
   - Required fields:
     - `id`
     - `from_boundary` / `to_boundary`
     - `scope`
     - `rationale`
     - `reviewer_signoff`
     - `tracking_issue`
     - `approved_on`
     - `expires_on`
     - `removal_expectations`

3. **Reviewer sign-off is mandatory**
   - PR must include architecture-owner approval.
   - `reviewer_signoff` must reference that approval context.

4. **Set expiry and removal expectation**
   - Exceptions are temporary.
   - `expires_on` must be after `approved_on` and not already expired.
   - Removal should happen in the refactor PR that eliminates the edge.

5. **Update release notes/changelog**
   - Document why the exception exists and expected removal window.

6. **Run fast-fail checks before push**

```bash
make preflight GO=go
```

## Validation entrypoints

```bash
# Direct compatibility + boundary guard checks
go test ./internal/controlplane/compat -count=1

# Fast-fail architecture gate (local and CI preflight)
make architecture-guard GO=go

# Contributor preflight (guardrails then full tests)
make preflight GO=go
```
