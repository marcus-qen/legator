# Legator

**A self-hosted fleet management control plane that deploys AI probes across heterogeneous infrastructure.**

Conversational management. Policy-driven guardrails. Audited change tracking. No vendor lock-in.

> "Zabbix meets ChatGPT with the policy engine of a bank."

## What is this?

Legator is a single control-plane application that:

- **Deploys "probe agents"** onto your servers, VMs, containers, or cloud accounts
- **Shows you everything** — health, inventory, activity, risk level per host
- **Lets you talk to each probe** in persistent two-way chat ("restart nginx on web-03")
- **Enforces strict guardrails** — read-only by default, graduated autonomy, human approval for changes
- **Audits every action** with before/after state, who approved what, full timeline

The LLM never touches your servers directly. The probe never reasons independently. **Brain and hands are separate.**

## Architecture

```
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

- **Control plane**: standalone Go binary, runs anywhere (bare metal, VM, container)
- **Probe**: static Go binary (~15-20MB), zero dependencies, systemd service
- **Connection**: persistent WebSocket, heartbeat every 30s, auto-reconnect

## Quick Start

### 1. Start the control plane

```bash
./control-plane
# Listening on :8080
```

### 2. Install a probe

```bash
curl -sSL https://your-server:8080/install.sh | sudo bash -s -- \
  --server https://your-server:8080 \
  --token <registration-token>
```

That's it. The probe registers, runs an inventory scan, and starts reporting.

### 3. Talk to your infrastructure

Open the web UI, click a probe, and start a conversation:

> "What's using all the disk on this server?"
> "Show me failed services in the last hour"
> "Draft a plan to upgrade nginx to the latest version"

## Guardrails

Three capability levels, enforced at **both** control plane and probe:

| Level | What it can do | Human approval? |
|-------|---------------|-----------------|
| **Observe** | Inventory, logs, file reads, process listing | No |
| **Diagnose** | Observe + analysis commands, config reads | No |
| **Remediate** | Diagnose + writes, restarts, patches | **Yes** |

Defence in depth: a compromised control plane cannot instruct a probe to exceed its configured capability level.

## Supported Targets

| Target | Method | Status |
|--------|--------|--------|
| Linux (bare metal / VM) | Installed probe (systemd) | **MVP** |
| Linux (remote) | SSH from control plane | Planned |
| Windows | Installed probe (Windows service) | Planned |
| Kubernetes | DaemonSet / operator | Planned |
| Network devices | SSH/API | Planned |
| Cloud accounts | AWS/Azure/GCP API | Planned |

## Project Structure

```
cmd/
  control-plane/    # Control plane binary
  probe/            # Probe agent binary
internal/
  controlplane/     # API, policy, WebSocket, fleet management, audit
  probe/            # Agent loop, inventory, executor, connection
  shared/           # Reusable: guardrails, rate limiting, security, telemetry
  protocol/         # Wire protocol (shared types between CP and probe)
install/            # One-liner install script
web/                # UI templates and static assets
docs/               # Documentation
test/               # Integration and E2E tests
```

## Building

```bash
make                  # Build both binaries
make build-probe-all  # Cross-compile probe for linux/amd64, linux/arm64, darwin/arm64
make test             # Run tests
```

## Status

**Early development.** The K8s-native agent runtime (v0.1–v0.9.2) has been archived to `archive/k8s-runtime`. The universal control plane is being built from the ground up, reusing the guardrail engine, policy evaluator, and audit trail.

## License

[Apache 2.0](LICENSE)

---

*"One who delegates." — Legator*
