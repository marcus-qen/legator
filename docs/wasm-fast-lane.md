# WASM Fast Lane

The WASM fast lane is a constrained sandbox execution tier designed for
lightweight, deterministic microtasks — linting, JSON transformation, schema
validation, and similar compute-bound operations. It runs workloads inside a
[wasmtime](https://wasmtime.dev/) runtime class with a reduced capability set
and tight resource limits, enabling faster startup and lower overhead compared
to a full kata-containers VM.

---

## When to Use WASM vs Full Linux Sandbox

| Criterion                         | WASM Fast Lane                         | Kata / Full Linux Sandbox            |
|-----------------------------------|----------------------------------------|--------------------------------------|
| **Workload type**                 | Deterministic microtasks (lint, transform, validate) | Arbitrary shell commands, long-running agents |
| **Network access**                | ❌ Not permitted                       | ✅ Configurable                      |
| **Host filesystem writes**        | ❌ Not permitted                       | ✅ Configurable (with approval)      |
| **Process spawning**              | ❌ Not permitted                       | ✅ Permitted                         |
| **Raw socket / syscall access**   | ❌ Blocked                            | ✅ Configurable                      |
| **Memory ceiling**                | 256 MiB (default)                     | 1024 MiB (default)                   |
| **CPU limit**                     | 500m (default)                        | 2000m (default)                      |
| **Max runtime**                   | 300 s                                  | Up to 3600 s                         |
| **Startup overhead**              | Low (wasmtime JIT)                    | Higher (VM boot)                     |
| **Approval required**             | mutation_gate (same as kata)           | mutation_gate                        |
| **Use when…**                     | Input → transform → output, no side-effects | Remediation, package installs, multi-process workflows |

**Rule of thumb:** if your task reads inputs and writes structured output
without touching the network or host filesystem, start with the WASM lane.
If your task needs a shell, a package manager, or a running daemon, use kata.

---

## Quick Start

### Create a WASM lane session

```http
POST /api/v1/sandboxes
Content-Type: application/json

{
  "workspace_id": "my-org",
  "probe_id": "probe-abc123",
  "lane": "wasm",
  "created_by": "alice"
}
```

The response includes `runtime_class: "wasmtime"`, `template_id: "wasm-fast-lane"`,
and metadata keys `lane_cpu_millis` and `lane_memory_mib`.

### Submit a task using a WASM template

```http
POST /api/v1/sandboxes/{id}/tasks
Content-Type: application/json

{
  "kind": "command",
  "command": ["lint", "--format=json", "/inputs/main.go"],
  "timeout_secs": 60
}
```

---

## Template Registry

Templates define a named, versioned specification for a sandbox execution
environment. WASM lane templates carry extra constraints:

| Field                | Type       | Description                                              |
|----------------------|------------|----------------------------------------------------------|
| `id`                 | string     | Unique template identifier                               |
| `workspace_id`       | string     | Owning workspace (`""` = global built-in)                |
| `name`               | string     | Human-readable name                                      |
| `runtime_class`      | string     | `wasmtime`, `kata-containers`, or `runc`                 |
| `capabilities`       | []string   | Explicitly permitted capabilities (empty = minimum)      |
| `denied_capabilities`| []string   | Explicitly blocked capabilities                          |
| `cpu_millis`         | int        | CPU limit in millicores                                  |
| `memory_mib`         | int        | Memory limit in MiB                                      |
| `max_run_secs`       | int        | Maximum task runtime in seconds                          |
| `metadata`           | object     | User-defined annotations                                 |

Templates are stored in SQLite and scoped to a workspace. Built-in templates
(with `workspace_id: ""`) are visible to all workspaces.

### Built-in WASM Templates

#### `wasm-lint-check`

Runs a WASM linter against input files and emits a JSON findings report.

```json
{
  "id": "wasm-lint-check",
  "name": "WASM Lint Check",
  "runtime_class": "wasmtime",
  "capabilities": ["host_fs_read"],
  "denied_capabilities": ["network", "host_fs_write", "syscall_unrestricted"],
  "cpu_millis": 250,
  "memory_mib": 128,
  "max_run_secs": 60,
  "metadata": {
    "task_kind": "lint",
    "output_type": "json_findings",
    "lane": "wasm"
  }
}
```

**Input:** source files mounted read-only  
**Output:** JSON array of `{ file, line, col, message, severity }`

#### `wasm-json-transform`

Applies a declarative transform spec to an input JSON document and emits the
result. Useful for config normalisation, schema migrations, or data masking.

```json
{
  "id": "wasm-json-transform",
  "name": "WASM JSON Transform",
  "runtime_class": "wasmtime",
  "capabilities": [],
  "denied_capabilities": ["network", "host_fs_write", "syscall_unrestricted"],
  "cpu_millis": 250,
  "memory_mib": 64,
  "max_run_secs": 30,
  "metadata": {
    "task_kind": "transform",
    "output_type": "json",
    "lane": "wasm"
  }
}
```

**Input:** `{ "input": {...}, "spec": {...} }`  
**Output:** transformed JSON document

---

## Capability Reference

Capabilities are strings that declare what a template is permitted to do.
The WASM lane enforces strict defaults; the following capabilities apply:

| Capability               | Permitted in WASM | Notes                                    |
|--------------------------|:-----------------:|------------------------------------------|
| `host_fs_read`           | ✅               | Read-only access to mounted inputs       |
| `host_fs_write`          | ❌               | Denied — use output channels instead     |
| `network`                | ❌               | No outbound TCP/UDP                      |
| `process_spawn`          | ❌               | WASM modules may not exec sub-processes  |
| `syscall_unrestricted`   | ❌               | Syscall set is locked to wasmtime seccomp|
| `raw_socket`             | ❌               | No raw socket access                     |
| `mount`                  | ❌               | No volume mount (beyond seeded inputs)   |
| `exec_host_process`      | ❌               | No host-side process execution           |

Attempting to create a template with a denied capability returns a validation
error before the template is persisted.

---

## Policy Integration

The WASM fast lane is enforced at two layers:

### 1. Policy template (`wasm-fast-lane`)

The built-in policy template `wasm-fast-lane` sets:

- `execution_class_required: wasm_sandbox`
- `sandbox_required: true`
- `approval_mode: mutation_gate`
- `max_runtime_sec: 300`
- `allowed_scopes: [wasm:exec, wasm:read]`
- `runtime_class: wasmtime`

This is automatically applied when you create a session with `lane: wasm`.

### 2. Runtime enforcement (`IsWasmLane` / `WasmLaneRejectsHostDirect`)

The policy engine evaluates `sandbox.runtime_class` at request time:

- `IsWasmLane(execClass)` — returns true for `wasm_sandbox` execution class
- `WasmLaneRejectsHostDirect(sandboxLane, requestedLane, category)` — blocks
  host-direct mutations from WASM-lane sandboxes

This means a session running in the WASM lane cannot escalate to a host-direct
execution path for mutation operations, even with a breakglass token.

---

## Performance Benchmarks

Benchmarks run on a 6-core Ryzen 5 3600 (SQLite WAL mode, in-memory DB):

| Benchmark                            | WASM              | Kata              | Notes                           |
|--------------------------------------|-------------------|-------------------|---------------------------------|
| Template create (instantiation)      | ~7.4 ms/op        | ~7.6 ms/op        | SQLite write dominates          |
| Template get (lookup)                | ~38 µs/op         | ~50 µs/op         | WASM read ~24% faster           |
| Template list (10 items)             | ~213 µs/op        | ~203 µs/op        | Comparable list scan overhead   |
| Capability validation (inline check) | ~64 ns/op         | ~2 ns/op          | WASM check loop vs. no-op       |
| Denied operation check               | ~6 ns/op (deny)   | N/A               | Linear scan over 5 deny rules   |

**Interpretation:**
- Template instantiation cost is dominated by the SQLite write, not runtime
  class; both lanes are equivalent at ~7-8 ms for a cold store operation.
- Template get (lookup) is faster for WASM because built-ins are always
  seeded; no workspace-scoped filter round-trip is needed for global templates.
- Capability validation adds ~64 ns for WASM (small linear scan over the
  restricted set); this is negligible at task submission time.
- Full VM boot overhead for kata is not represented here (these benchmarks
  measure the template registry layer only). In practice, wasmtime JIT startup
  is measured in milliseconds vs. hundreds of milliseconds for a kata VM.

---

## Authoring Custom WASM Templates

To define a workspace-scoped WASM template via the API:

```http
POST /api/v1/sandboxes/templates
Content-Type: application/json

{
  "workspace_id": "my-org",
  "name": "schema-validator",
  "description": "Validates config YAML against a JSON Schema",
  "runtime_class": "wasmtime",
  "capabilities": ["host_fs_read"],
  "cpu_millis": 150,
  "memory_mib": 64,
  "max_run_secs": 30,
  "metadata": {
    "task_kind": "validate",
    "output_type": "json"
  }
}
```

**Validation rules:**
- `runtime_class` must be one of `wasmtime`, `kata-containers`, `runc`
- For `wasmtime`: `capabilities` must not include `network`, `host_fs_write`,
  or `syscall_unrestricted`
- `cpu_millis`, `memory_mib`, `max_run_secs` default to `0` (no explicit limit)

---

## Architecture Notes

```
┌────────────────────────────────────────────────────────────┐
│                    Sandbox API Layer                       │
│  POST /api/v1/sandboxes  (lane=wasm → runtime=wasmtime)   │
└──────────────────────────┬─────────────────────────────────┘
                           │
              ┌────────────▼─────────────┐
              │      Lane Resolver       │
              │  LaneWasm → LaneDefaults │
              │  (runtime_class, limits) │
              └────────────┬─────────────┘
                           │
         ┌─────────────────▼──────────────────────┐
         │         TemplateStore (SQLite)          │
         │  wasm-lint-check / wasm-json-transform  │
         │  Custom workspace templates (CRUD)      │
         └─────────────────┬──────────────────────┘
                           │
         ┌─────────────────▼──────────────────────┐
         │         Policy Engine                   │
         │  wasm-fast-lane template                │
         │  IsWasmLane() enforcement               │
         │  WasmLaneRejectsHostDirect()            │
         └────────────────────────────────────────┘
```

The WASM fast lane slots into the same sandbox session lifecycle
(created → provisioning → ready → running → destroyed) as any other lane.
The runtime enforcement is transparent to callers — they interact with the
standard sandbox API and the lane parameter handles the rest.

---

## See Also

- `internal/controlplane/sandbox/lane.go` — Lane registry and defaults
- `internal/controlplane/sandbox/template.go` — TemplateStore with SQLite CRUD
- `internal/controlplane/sandbox/wasm_lane.go` — WASM constants and validators
- `internal/controlplane/policy/templates.go` — Built-in policy templates
- `internal/controlplane/policy/sandbox_enforcement.go` — Runtime deny rules
