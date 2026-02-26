# Legator

**A self-hosted fleet management control plane that deploys AI probes across heterogeneous infrastructure.**

Conversational management. Policy-driven guardrails. Audited change tracking. No vendor lock-in.

> "Zabbix meets ChatGPT with the policy engine of a bank."

## What is this?

Legator is a single control-plane application that:

- **Deploys probe agents** onto your servers, VMs, containers, or cloud accounts
- **Shows you everything** — health, inventory, activity, risk level per host
- **Lets you talk to each probe** in persistent two-way chat ("restart nginx on web-03")
- **Enforces strict guardrails** — read-only by default, graduated autonomy, human approval for changes
- **Audits every action** with before/after state, who approved what, full timeline

The LLM never touches your servers directly. The probe never reasons independently. **Brain and hands are separate.**

## Architecture

```text
┌─────────────────────────────────────────────────────┐
│                  Control Plane                       │
│  Web UI · REST API · WebSocket Server · LLM Brain   │
│  Policy Engine · Approval Queue · Audit Log          │
└────────────────────┬────────────────────────────────┘
                     │ WSS (TLS)
          ┌──────────┼──────────┐
          ▼          ▼          ▼
      ┌───────┐  ┌───────┐  ┌───────┐
      │ Probe │  │ Probe │  │ Probe │
      │ web-01│  │ db-01 │  │ k8s-03│
      └───────┘  └───────┘  └───────┘
```

- **Control plane**: standalone Go binary (14MB), runs anywhere
- **Probe**: static Go binary (7MB), zero dependencies, systemd service
- **legatorctl**: CLI for fleet management from the terminal
- **Connection**: persistent WebSocket, heartbeat every 30s, auto-reconnect with jitter

See [docs/architecture.md](docs/architecture.md) for the full component breakdown.

## Quick Start

### 1. Build

```bash
git clone https://github.com/marcus-qen/legator.git
cd legator
make all
```

### 2. Start the control plane

```bash
# Minimal — in-memory state, auto-generated signing key
./bin/control-plane

# Production — persistent SQLite, auth enabled
LEGATOR_DATA_DIR=/var/lib/legator \
LEGATOR_SIGNING_KEY=$(openssl rand -hex 32) \
LEGATOR_AUTH=true \
./bin/control-plane
```

### 3. Connect a probe

```bash
# Generate a registration token
TOKEN=$(curl -sf -X POST http://localhost:8080/api/v1/tokens | jq -r .token)

# One-liner install (production)
curl -sSL http://localhost:8080/install.sh | sudo bash -s -- \
  --server http://localhost:8080 --token "$TOKEN"

# Or manual (development)
./bin/probe run --server http://localhost:8080 --token "$TOKEN"
```

### 4. See your fleet

Open `http://localhost:8080/` in a browser, or:

```bash
curl -sf http://localhost:8080/api/v1/fleet/summary | jq
```

📖 **Full guide:** [docs/getting-started.md](docs/getting-started.md)

## Features

| Feature | Status |
|---|---|
| Fleet view (web UI) | ✅ |
| Per-probe chat (LLM-powered) | ✅ |
| Policy engine (observe/diagnose/remediate) | ✅ |
| Defence-in-depth policy enforcement | ✅ |
| Approval queue with risk classification | ✅ |
| Immutable audit log | ✅ |
| HMAC-SHA256 command signing | ✅ |
| Output streaming (SSE) | ✅ |
| Probe self-update | ✅ |
| Tags + group commands | ✅ |
| Multi-user RBAC | ✅ |
| API key management | ✅ |
| Webhook notifications | ✅ |
| Prometheus metrics | ✅ |
| Health scoring | ✅ |
| CI/CD (test + lint + e2e + release) | ✅ |

## Configuration

| Variable | Default | Description |
|---|---|---|
| `LEGATOR_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `LEGATOR_DATA_DIR` | `/var/lib/legator` | SQLite databases location |
| `LEGATOR_SIGNING_KEY` | auto-generated | HMAC key for command signing (hex, 64+ chars) |
| `LEGATOR_AUTH` | `false` | Enable authentication |
| `LEGATOR_LLM_PROVIDER` | — | LLM provider (e.g. `openai`) |
| `LEGATOR_LLM_BASE_URL` | — | LLM API base URL |
| `LEGATOR_LLM_API_KEY` | — | LLM API key |
| `LEGATOR_LLM_MODEL` | — | LLM model name |

## Building

```bash
make all              # Build control-plane, probe, legatorctl
make test             # Run unit tests
make e2e              # Full end-to-end flow (29+ checks)
make lint             # golangci-lint
make build-release    # Cross-compile release binaries (linux amd64+arm64)
```

## API

35+ REST endpoints. Key surface areas:

- **Fleet**: `GET /api/v1/probes`, `GET /api/v1/fleet/summary`, `POST /api/v1/probes/{id}/command`
- **Chat**: `GET/POST /api/v1/probes/{id}/chat`, `GET /ws/chat`
- **Policy**: `GET/POST /api/v1/policies`, `POST /api/v1/probes/{id}/apply-policy/{policyId}`
- **Approvals**: `GET /api/v1/approvals`, `POST /api/v1/approvals/{id}/decide`
- **Audit**: `GET /api/v1/audit`
- **Webhooks**: `GET/POST /api/v1/webhooks`
- **Auth**: `GET/POST/DELETE /api/v1/auth/keys`, `GET/POST/DELETE /api/v1/users`
- **Metrics**: `GET /api/v1/metrics`
- **Events**: `GET /api/v1/events` (SSE stream)

## Status

**v1.0.0-alpha.4** — working control plane + probe runtime with multi-user RBAC.

~106 Go files · 30 test suites · 29/29 e2e · lint clean

## License

[Apache 2.0](LICENSE)

---

*"One who delegates." — Legator*
