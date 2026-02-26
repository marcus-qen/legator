# Legator Architecture

## Design Principle

**Separate the brain from the hands.**

- The control plane decides (LLM reasoning, policy evaluation, approval workflows)
- The probe executes only what policy allows
- Every action is observable, auditable, and reversible where possible
- The LLM never touches target servers directly

## Components

```text
┌────────────────────────────────────────────────────────────────┐
│                       Control Plane                            │
│                                                                │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐     │
│  │  Web UI  │  │ REST API │  │ WS Hub   │  │ LLM      │     │
│  │ (HTML)   │  │ (HTTP)   │  │ (probe   │  │ Provider │     │
│  │          │  │          │  │  conns)  │  │          │     │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘     │
│       │              │              │              │           │
│  ┌────┴──────────────┴──────────────┴──────────────┴─────┐    │
│  │                    Server (routes.go)                  │    │
│  └──┬────┬────┬────┬────┬────┬────┬────┬────┬────┬───┘    │
│     │    │    │    │    │    │    │    │    │    │         │
│  ┌──┴┐ ┌┴──┐ ┌┴──┐ ┌┴──┐ ┌┴──┐ ┌┴──┐ ┌┴──┐ ┌┴──┐      │
│  │Flt│ │Aud│ │App│ │Pol│ │Cht│ │Evt│ │Mtx│ │Whk│      │
│  │Mgr│ │Log│ │Que│ │Str│ │Str│ │Bus│ │Col│ │Not│      │
│  └──┬┘ └──┬┘ └──┬┘ └──┬┘ └──┬┘ └───┘ └───┘ └──┬┘      │
│     │     │     │     │     │                    │        │
│  ┌──┴─────┴─────┴─────┴─────┴────────────────────┴──┐     │
│  │              SQLite (WAL mode)                     │     │
│  │  fleet.db · audit.db · chat.db · policy.db        │     │
│  │  webhook.db · auth.db                              │     │
│  └────────────────────────────────────────────────────┘     │
└────────────────────────────────────────────────────────────────┘
        │                    │                    │
        │ WSS               │ WSS               │ WSS
        ▼                    ▼                    ▼
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│  Probe Agent │  │  Probe Agent │  │  Probe Agent │
│              │  │              │  │              │
│ ┌──────────┐ │  │ ┌──────────┐ │  │ ┌──────────┐ │
│ │Connection│ │  │ │Connection│ │  │ │Connection│ │
│ │ (WS+HB)  │ │  │ │ (WS+HB)  │ │  │ │ (WS+HB)  │ │
│ ├──────────┤ │  │ ├──────────┤ │  │ ├──────────┤ │
│ │ Executor │ │  │ │ Executor │ │  │ │ Executor │ │
│ │ +Policy  │ │  │ │ +Policy  │ │  │ │ +Policy  │ │
│ ├──────────┤ │  │ ├──────────┤ │  │ ├──────────┤ │
│ │Inventory │ │  │ │Inventory │ │  │ │Inventory │ │
│ ├──────────┤ │  │ ├──────────┤ │  │ ├──────────┤ │
│ │ FileOps  │ │  │ │ FileOps  │ │  │ │ FileOps  │ │
│ ├──────────┤ │  │ ├──────────┤ │  │ ├──────────┤ │
│ │ Updater  │ │  │ │ Updater  │ │  │ │ Updater  │ │
│ └──────────┘ │  │ └──────────┘ │  │ └──────────┘ │
│   Linux VM   │  │  K8s Node    │  │  Bare Metal  │
└──────────────┘  └──────────────┘  └──────────────┘
```

**Legend:** Flt=Fleet Manager, Aud=Audit Log, App=Approval Queue, Pol=Policy Store, Cht=Chat Store, Evt=Event Bus, Mtx=Metrics Collector, Whk=Webhook Notifier

## Control Plane

Single Go binary (~14MB). No external dependencies beyond optional LLM API.

### Subsystems

| Package | Purpose |
|---|---|
| `server/` | HTTP server, route wiring, template rendering, auth middleware |
| `fleet/` | Probe registry, heartbeat tracking, inventory, health scoring, SQLite store |
| `websocket/` | Hub: per-probe WebSocket connections, command signing, stream registry |
| `llm/` | OpenAI-compatible provider, agentic task runner, chat responder |
| `approval/` | Risk-gated approval queue, risk classifier, submit/decide/reaper |
| `audit/` | Immutable audit log, SQLite-backed with WAL + indexes |
| `chat/` | Per-probe persistent chat sessions, REST + WebSocket handlers |
| `policy/` | Policy template CRUD, 3 built-in templates + custom |
| `auth/` | API key auth, session auth, RBAC with role-permission mapping |
| `events/` | Pub/sub event bus for fleet-wide events |
| `metrics/` | Prometheus-compatible metrics collector |
| `webhook/` | Webhook notifier, HMAC signing, delivery tracking |
| `cmdtracker/` | In-flight command tracking, sync result routing |
| `api/` | Registration + token handlers |
| `config/` | Config file + env var loading |

### Data Stores

All SQLite with WAL mode for concurrent reads:

- **fleet.db** — probe state, heartbeats, inventory, tags
- **audit.db** — immutable audit events (indexed by time, probe, type)
- **chat.db** — per-probe chat history
- **policy.db** — policy templates
- **webhook.db** — webhook configs + delivery log
- **auth.db** — API keys, users, sessions

### Authentication

Dual-path when `LEGATOR_AUTH=true`:
1. **API keys** — `Authorization: Bearer lgk_...` for programmatic access
2. **Session cookies** — browser login via `/login` page

Role-based access control:
- **admin** — full access, user management, API key management
- **operator** — fleet management, command execution, approvals
- **viewer** — read-only access to fleet, audit, metrics

## Probe Agent

Static Go binary (~7MB). Zero runtime dependencies.

### Subsystems

| Package | Purpose |
|---|---|
| `agent/` | Main loop, config loading, message dispatch |
| `connection/` | WebSocket client, heartbeat, auto-reconnect with exponential backoff + jitter |
| `executor/` | Command execution, output streaming, local policy enforcement |
| `inventory/` | System scanner (OS, CPU, RAM, disk, packages, services, users, interfaces) |
| `fileops/` | Guarded file read/search/stat/readlines |
| `updater/` | Self-update: download, SHA256 verify, atomic binary swap, restart |
| `status/` | Local health status endpoint |

### Probe Lifecycle

```
1. probe init --server URL --token TOKEN
   → POST /api/v1/register (consumes token)
   → Receives: probe_id, api_key, policy_id
   → Writes config.yaml

2. probe run (or systemd starts it)
   → Connects WSS to control plane
   → Authenticates with probe_id + api_key
   → Sends initial inventory
   → Enters heartbeat + message loop

3. Ongoing:
   → Heartbeat every 30s (CPU, mem, disk, load)
   → Inventory refresh every 15min
   → Receives commands, executes with policy check
   → Receives policy updates, applies immediately
   → Self-updates when instructed
```

## Wire Protocol

All messages wrapped in `Envelope` with JSON encoding over WebSocket:

```json
{
  "id": "unique-message-id",
  "type": "command",
  "timestamp": "2026-02-26T12:00:00Z",
  "payload": { ... },
  "signature": "hmac-sha256-hex"
}
```

### Message Types

| Type | Direction | Purpose |
|---|---|---|
| `register` | Probe → CP | Initial registration |
| `registered` | CP → Probe | Registration response |
| `heartbeat` | Probe → CP | Health metrics (30s) |
| `inventory` | Probe → CP | Full system inventory |
| `command` | CP → Probe | Execute a command |
| `command_result` | Probe → CP | Command output/exit code |
| `output_chunk` | Probe → CP | Streaming output |
| `policy_update` | CP → Probe | Push new policy |
| `update` | CP → Probe | Binary self-update |
| `key_rotation` | CP → Probe | Rotate probe API key |
| `ping`/`pong` | Bidirectional | Connection keepalive |

### Command Signing

Every command from the control plane includes an HMAC-SHA256 signature:
1. Master signing key set on CP (or auto-generated)
2. Per-probe key derived: `HMAC(master_key, probe_id)`
3. Signature covers: `message_id + command_payload`
4. Probe verifies before executing (when signing enabled)
5. Invalid/missing signatures → command rejected

## Security Model

### Defence in Depth

Policy is enforced at **two independent layers**:

1. **Control plane** — checks policy before sending commands
2. **Probe** — checks its LOCAL policy before executing

A compromised control plane cannot instruct a probe to exceed its configured capability level.

### Capability Levels

| Level | Scope | Risk |
|---|---|---|
| **observe** | Read-only (ls, cat, ps, df) | None |
| **diagnose** | observe + analysis (strace, tcpdump, dig) | Low |
| **remediate** | diagnose + writes (rm, apt, systemctl restart) | High — requires approval |

### Authentication Chain

```
Probe → WSS → Control Plane
  auth: probe_id + api_key (per-probe, unique)
  signing: HMAC-SHA256 per-command

User → HTTPS → Control Plane
  auth: API key (Bearer) OR session cookie
  RBAC: role → permissions → route guard
```

### Key Security Features

- Single-use registration tokens (HMAC-signed, 30min expiry)
- Per-probe API keys (cryptographically random, 32 bytes)
- HMAC command signing (prevents MITM injection)
- API key rotation pushed to probes
- Credential sanitisation in audit logs
- Hardened systemd units (NoNewPrivileges, ProtectSystem, etc.)

## Deployment

### Minimum Production Setup

```
[Reverse Proxy (Caddy/nginx)]  ← TLS termination
         │
  [Control Plane]  ← port 9080, systemd service
         │
  [SQLite DBs]     ← /var/lib/legator/
```

### Recommended Production Setup

```
[Caddy]  ← TLS via Let's Encrypt / Cloudflare
    │
[Control Plane]  ← systemd, auth enabled, LLM configured
    │
[SQLite]  ← /var/lib/legator/ (backed up daily)
    │
[N probes]  ← systemd services on target machines
```

No Kubernetes required. No Docker required. No external database. Just Go binaries and SQLite.

---

*~30 Go packages, ~106 files, 29 e2e tests, lint clean.*
