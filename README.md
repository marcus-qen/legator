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

## Quick Start

### 1. Start the control plane

```bash
# Minimal — in-memory state, auto-generated signing key
./control-plane

# Production — persistent SQLite, custom signing key, auth enabled
LEGATOR_DATA_DIR=/var/lib/legator \
LEGATOR_SIGNING_KEY=$(openssl rand -hex 32) \
LEGATOR_AUTH=true \
./control-plane
```

### 2. Generate a registration token

```bash
curl -sf -X POST http://localhost:8080/api/v1/tokens | jq .token
```

### 3. Install a probe

```bash
curl -sSL https://your-server:8080/install.sh | sudo bash -s -- \
  --server https://your-server:8080 \
  --token <registration-token>
```

Or manually:

```bash
./probe run --server http://your-server:8080 --token <token>
```

The probe registers, runs an inventory scan, and starts reporting.

## Building

```bash
make all            # Build control-plane, probe, legatorctl
make test           # Run unit tests
make e2e            # Full end-to-end flow (27 checks)
make build-all      # Cross-compile release binaries
```

## Status

✅ **Working control plane + probe runtime.**

The K8s-native runtime (v0.1–v0.9.2) is archived on `archive/k8s-runtime`. Current `main` is the standalone universal control plane with:

- persistent SQLite stores (fleet, audit, chat, webhook, policy, auth)
- approval workflow + audit trail
- LLM task and chat integration
- metrics + webhooks + key rotation + update pipeline
- green unit tests and green end-to-end suite (27/27)

## License

[Apache 2.0](LICENSE)

---

*"One who delegates." — Legator*
