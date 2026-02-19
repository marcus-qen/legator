# InfraAgent

**Kubernetes operator for autonomous infrastructure agents — lights-out cluster management with hard safety guarantees.**

> ⚠️ This project is in active development. Not yet ready for production use.

## What Is This?

InfraAgent is a Kubernetes operator that runs autonomous LLM-powered agents to manage your infrastructure. Install it on any cluster, apply CRDs defining your agents, and they start monitoring, triaging, deploying, and remediating — automatically, safely, and with a complete audit trail.

## Design Principles

1. **The LLM runs the cluster lights-out** — no human in the loop for routine operations
2. **Hyper-aware of non-destructive actions** — every mutation is assessed before execution
3. **Services must be *functional*, not just *up*** — agents verify real behaviour, not just pod status
4. **Data is sacred** — data mutations are NEVER automated, no override, no exception
5. **Audit everything** — every action, every decision, every block — immutable record
6. **Portable** — same agent + same skills + different environment = different cluster

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                    infraagent-controller (Go)                        │
│                                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────────────┐ │
│  │ CRD Watcher  │  │  Schedule   │  │       Agent Runner           │ │
│  │             │  │  Controller │  │                              │ │
│  │ InfraAgent  │→ │  Cron       │→ │  1. Assemble prompt          │ │
│  │ AgentEnv    │  │  Interval   │  │  2. Match Action Sheet       │ │
│  │ ModelTier   │  │  Webhook    │  │  3. Call LLM                 │ │
│  └─────────────┘  └─────────────┘  │  4. Pre-flight check action  │ │
│                                     │  5. Enforce guardrails       │ │
│                                     │  6. Execute or block         │ │
│                                     │  7. Log to AgentRun          │ │
│                                     │  8. Report findings          │ │
│                                     └──────────────────────────────┘ │
│                                                                      │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │  Guardrail Engine                                            │    │
│  │  • Four-tier action classification (read/service/destructive │    │
│  │    /data-mutation)                                           │    │
│  │  • Graduated autonomy (observe→recommend→automate→full)      │    │
│  │  • Data protection: PVC/PV/namespace/DB deletion blocked     │    │
│  │    unconditionally (no config can override)                  │    │
│  │  • Action Sheets: undeclared action = denied                 │    │
│  │  • Pre-flight checks, cooldowns, budget enforcement          │    │
│  └──────────────────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────────────────┘
         │              │              │              │
         ▼              ▼              ▼              ▼
    K8s API        MCP Servers     LLM APIs     Notification
    (client-go)    (k8sgpt etc)   (pluggable)    Channels
```

## CRDs

| CRD | Purpose | Scope |
|-----|---------|-------|
| **InfraAgent** | Agent definition — identity, schedule, model tier, guardrails, skills | Namespaced |
| **AgentEnvironment** | Site binding — endpoints, credentials, namespaces, data resources, MCP servers | Namespaced |
| **ModelTierConfig** | Maps tier names (fast/standard/reasoning) → provider/model + auth | Cluster |
| **AgentRun** | Immutable audit record — every action, pre-flight check, block, escalation | Namespaced |

## Quick Start

```bash
# Install the operator
helm install infraagent charts/infraagent/ -n infraagent-system --create-namespace

# Configure model access
kubectl apply -f examples/model-tier-config.yaml

# Deploy an environment
kubectl apply -f examples/environments/dev-lab.yaml -n agents

# Deploy an agent
kubectl apply -f examples/agents/watchman-light.yaml -n agents

# Check status
kubectl get infraagents -n agents
kubectl get agentruns -n agents
```

## Safety Model

### Graduated Autonomy

| Level | Read | Recommend | Safe Mutations | Destructive Mutations |
|-------|------|-----------|----------------|----------------------|
| `observe` | ✅ | ❌ | ❌ | ❌ |
| `recommend` | ✅ | ✅ | ❌ | ❌ |
| `automate-safe` | ✅ | ✅ | ✅ | ❌ (escalate) |
| `automate-destructive` | ✅ | ✅ | ✅ | ✅ |

### Data Protection (Non-Configurable)

These rules are hardcoded in the runtime. No CRD field, no flag, no override can disable them:

- **Never** delete PVCs or PVs
- **Never** modify PV reclaim policy
- **Never** delete namespaces
- **Never** delete database cluster CRs
- **Never** delete S3 objects or buckets

When an agent concludes a data mutation is needed, the runtime blocks the action, logs the intent, and escalates to a human.

### Action Sheets

Skills declare every action they can perform in `actions.yaml`. The runtime enforces an **allowlist principle**: undeclared actions are denied. Each declared action includes its risk tier, pre-conditions, cooldown, and rollback strategy.

## Three-Layer Architecture

```
InfraAgent (what/when/guardrails)
  + Skills (expertise — Agent Skills format)
  + AgentEnvironment (where — site-specific)
  = Runnable, scheduled, guardrailed agent
```

- **InfraAgent** is portable — deploy the same agent spec on any cluster
- **Skills** are portable — infrastructure-agnostic behavioral knowledge
- **AgentEnvironment** is site-specific — created per deployment target

## Model Tiers

Agents specify model needs as `fast`, `standard`, or `reasoning`. The `ModelTierConfig` CRD maps these to actual providers:

```yaml
tiers:
  - tier: fast
    provider: anthropic
    model: claude-haiku-3-5-20241022
  - tier: standard
    provider: anthropic
    model: claude-sonnet-4-20250514
  - tier: reasoning
    provider: anthropic
    model: claude-opus-4-20250514
```

Supports: Anthropic, OpenAI, Ollama, any OpenAI-compatible endpoint. Auth: API key, OAuth, ServiceAccount, custom headers.

## Documentation

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/getting-started.md) | Install → first agent in 10 minutes |
| [CRD Reference](docs/crd-reference.md) | Every field in every CRD |
| [Writing Skills](docs/skills-authoring.md) | Create custom agent expertise |
| [Environment Binding](docs/environment-binding.md) | Configure for your cluster |
| [Data Protection](docs/data-protection.md) | How data resources are protected |
| [Guardrails Deep-Dive](docs/guardrails.md) | The complete safety model |
| [Model Tier Config](docs/model-tier-config.md) | Provider and model setup |
| [Troubleshooting](docs/troubleshooting.md) | Common issues and fixes |
| [Architecture Decisions](docs/adr/) | Why we made the choices we did |
| [Contributing](CONTRIBUTING.md) | Development setup and PR process |

## Examples

| Example | Description |
|---------|-------------|
| [hello-world](examples/agents/hello-world.yaml) | Simplest possible agent |
| [watchman-light](examples/agents/watchman-light.yaml) | Endpoint monitoring (5-min cron) |
| [multi-cluster](examples/agents/multi-cluster-watchman.yaml) | Monitor a remote cluster |
| [All 10 agents](examples/agents/) | Full ops team (monitoring, deploy, triage, QA) |

## Status

**v0.1.0-dev** — All core phases complete (0–8). Documentation and distribution in progress.

Implemented: CRDs, assembler, runner, guardrail engine, Action Sheet enforcement, data protection, scheduler (cron/interval/webhook), reporting (Slack/Telegram/webhook), escalation, Prometheus metrics, OTel tracing, MCP integration (k8sgpt), skill distribution (Git/ConfigMap), multi-cluster, rate limiting, credential sanitization, graceful shutdown, AgentRun retention.

253 tests. Binary builds clean. See the [project plan](docs/PROJECT-PLAN.md) for the full roadmap.

## License

Apache License 2.0

---

*"We are what we repeatedly do. Excellence, then, is not an act, but a habit." — Aristotle (via Durant)*
