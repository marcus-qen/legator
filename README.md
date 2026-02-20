# InfraAgent

[![CI](https://github.com/marcus-qen/infraagent/actions/workflows/ci.yaml/badge.svg)](https://github.com/marcus-qen/infraagent/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/marcus-qen/infraagent)](https://goreportcard.com/report/github.com/marcus-qen/infraagent)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

**Kubernetes operator for autonomous infrastructure agents — lights-out cluster management with hard safety guarantees.**

> **v0.1.0** — Running in production on a 4-node Talos K8s cluster. 10 agents, 275+ runs, 82% success rate (improving daily). See [Dogfooding](#dogfooding) below.

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

## Dogfooding

InfraAgent is running on a 4-node Talos Kubernetes cluster, managing the same platform it was built on. Ten autonomous agents handle monitoring, deployment verification, issue triage, security auditing, and daily briefings — replacing manually-scheduled cron jobs.

### Fleet (live data from dogfooding)

| Agent | Role | Schedule | Tokens/run | Status |
|-------|------|----------|------------|--------|
| 🔍 watchman-light | Endpoint monitoring | */5 min | ~11K | ✅ 120+ runs |
| 🔍 watchman-deep | Deep infrastructure audit | Hourly | ~10K | ✅ |
| 🔭 scout | QA exploration | Daily 4AM | ~3K | ✅ |
| ⚖️ tribune | GitHub issue triage | Daily 7AM | ~17K | ✅ |
| 📊 analyst | Fleet performance review | Weekly | ~9K | ✅ |
| 📯 herald | Morning briefing | Daily 8AM | ~13K | ✅ |
| 🔨 forge | Deployment verification | */5 min | ~17K | ✅ 85+ runs |
| 🚀 promotion | Dev→prod drift detection | Every 6h | ~12K | ✅ |
| 🔒 config-backup | Config audit | Daily 1AM | ~13K | ✅ |
| 🔥 vigil | E2E platform verification | Daily 5AM | ~20K | ✅ |

**Observability**: Prometheus metrics (9 custom metrics), Grafana dashboard, Tempo traces, 4 alert rules.

**Provider flexibility**: Switched from Anthropic Sonnet to Kimi (`kimi-latest` via OpenAI-compatible endpoint) with zero code changes — just updated the `ModelTierConfig` CRD. Token usage dropped 60-70%.

### What We Learned

- **Token budgets matter**: Agents without explicit budgets waste 3-5x more tokens. Skills should specify tool call budgets.
- **Credential injection must override LLM headers**: LLMs read credential placeholders from skill text and set them as literal HTTP headers. The tool layer must always override.
- **Force-report on final iteration**: Without it, agents exhaust all iterations on data gathering and never synthesise a report.
- **Conversation pruning prevents quadratic growth**: A sliding window (first message + last 4 pairs) keeps token usage flat across iterations.

## Status

**v0.1.0** — Running in production. 10 phases of build, 2 phases of dogfooding complete.

Core: CRDs, prompt assembler, agentic runner, guardrail engine, Action Sheet enforcement, hardcoded data protection, cron/interval/webhook scheduler, conversation pruning, force-report, per-call token caps.

Safety: Four-tier action classification, graduated autonomy, undeclared-action denial, cooldowns, credential sanitization in audit trail.

Integration: Prometheus metrics (9 gauges/counters/histograms), OpenTelemetry tracing (GenAI conventions), Grafana dashboard, MCP client (k8sgpt), Git+ConfigMap skill loading.

Production: Multi-cluster support, rate limiting (per-agent + cluster-wide), AgentRun retention (TTL + preserve-min), graceful shutdown, leader election.

Providers: Anthropic, OpenAI, and any OpenAI-compatible endpoint (Kimi, Ollama, vLLM, etc.) with configurable base URL.

253 tests.

## License

Apache License 2.0

---

*"We are what we repeatedly do. Excellence, then, is not an act, but a habit." — Aristotle (via Durant)*
