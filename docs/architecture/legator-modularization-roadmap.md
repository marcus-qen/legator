# Legator Modularization Roadmap (Alpha.15+)

## Purpose

Turn Legator into a reusable platform kernel with composable adapters and replaceable interfaces ("Lego bricks"), so core capabilities can power multiple products/use-cases beyond host probes.

Primary next reuse target: research infrastructure workflows (Kubeflow jobs + Grafana capacity visibility).

---

## North Star

**Agent-first, human-governed control plane**

- Agents/API/MCP are primary execution interfaces
- UI is the operator cockpit for governance, approvals, incident triage, and trust
- Core logic is portable across surfaces and adapters

---

## Architectural Direction

### Layer model

1. **Core domain** (reusable, no transport concerns)
2. **Adapters** (external systems, capability-specific integration)
3. **Surfaces** (API, MCP, UI)
4. **Platform plumbing** (authn/config/storage/events)

### Dependency rule

- `surfaces -> core`
- `core -> adapter interfaces`
- `adapters -> external systems`
- **No domain logic in handlers/templates**

---

## Target Module Map

```text
/internal
  /core
    /identity        # RBAC primitives (users/roles/permissions)
    /policy          # policy evaluation + enforcement levels
    /approval        # approval queue + decision lifecycle
    /audit           # event model, query/export/retention
    /jobs            # async jobs, retries, status, cancellation
    /resource        # canonical resource model + metadata

  /adapters
    /probe           # host probe execution + inventory
    /networkdevice   # SSH/NETCONF execution + inventory
    /kubeflow        # job submit/list/status/cancel (planned)
    /grafana         # capacity/availability signals (planned)

  /surfaces
    /api             # REST handlers (thin)
    /mcp             # MCP tools/resources bindings (thin)
    /web             # templates/view-models only (thin)

  /platform
    /authn           # session/OIDC/API key middleware
    /config          # runtime configuration
    /storage         # store interfaces + sqlite implementations
    /events          # pub/sub and event fan-out
```

---

## Milestone Plan

## Alpha.15 — Kernel Split

### Scope
- Extract and stabilize `/internal/core/*` use-cases from route handlers
- Introduce explicit interfaces between core and adapters
- Keep runtime behavior unchanged (refactor-only)

### Deliverables
- Core service boundaries for: approval, policy, audit, resource orchestration
- Surface handlers reduced to transport + validation + mapping
- Architecture tests/lints to block business logic in surfaces

### Acceptance Criteria
- No direct business decisions in `server/routes*.go` or template scripts
- Existing API + MCP + UI behavior unchanged
- Full test suite green

---

## Alpha.16 — Async Execution Backbone

### Scope
- Add `core/jobs` for long-running actions
- Move scan/inventory/test flows to asynchronous job execution
- Add cancellable job states and retry policy

### Deliverables
- Job schema + queue + status endpoints
- Job event emission into audit stream
- UI and MCP support for job polling/streaming

### Acceptance Criteria
- No long-running task blocks request lifecycle
- Action outcomes fully auditable via job IDs
- Retries/backoff configurable and tested

---

## Alpha.17 — Kubeflow Adapter MVP

### Scope
- Introduce `adapters/kubeflow` for workload lifecycle
- Normalize Kubeflow entities into core resource model

### Deliverables
- Endpoints/tools for submit/list/status/cancel job
- Namespace/profile mapping and RBAC-aware actions
- Audit trail for workload operations

### Acceptance Criteria
- A user/agent can launch and track a Kubeflow job via Legator APIs/MCP
- Authorization and policy gates apply consistently

---

## Alpha.18 — Grafana Capacity Adapter + Policy Integration

### Scope
- Introduce `adapters/grafana` for read-only capacity telemetry
- Feed signals into policy decisions (allow/deny/queue)

### Deliverables
- Capacity snapshot endpoints/resources
- Policy explanations (why action denied/deferred)
- UI panels for operator decision support

### Acceptance Criteria
- Resource decisions include machine-readable rationale
- Capacity constraints are visible and testable end-to-end

---

## Cross-Cutting Engineering Standards

1. **Surface parity for new capabilities**
   - Every capability should expose:
     - API route(s)
     - MCP tool/resource
     - Audit events
     - Permission tests

2. **Security baseline**
   - No plaintext credentials in responses/logs
   - Secret references over embedded values
   - Explicit authorization-denied audit events for protected actions

3. **Test strategy**
   - Unit tests for stores/services
   - Permission matrix tests for routes
   - E2E path tests for each major capability

4. **Backward compatibility**
   - API + MCP contract versioning policy
   - Deprecation notes in changelog/docs

---

## Risks and Mitigations

### Risk: Refactor drift breaks velocity
- **Mitigation:** Alpha.15 is behavior-preserving extraction only; defer feature churn until boundaries settle.

### Risk: Adapter sprawl and inconsistency
- **Mitigation:** enforce adapter interface contracts and shared core resource model.

### Risk: Governance bypass through new surfaces
- **Mitigation:** all mutations route through core policy/approval/audit pipeline.

---

## Immediate Next Actions

1. Create Alpha.15 branch and carve out first core package (`core/approval` + `core/policy` service facade)
2. Add architecture guard tests to fail CI if domain logic leaks into surface handlers
3. Draft API/MCP compatibility policy doc (versioning + deprecation)
4. Open implementation cards for Alpha.15 scope slices

---

## Outcome

This roadmap keeps Legator self-hosted-first while making it modular enough to reuse across:
- host fleet operations,
- network device management,
- research workload orchestration (Kubeflow),
- capacity-aware governance (Grafana signals).

The result is not "another portal", but a reusable control-plane kernel with multiple operator and agent-facing surfaces.
