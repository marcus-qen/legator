# Legator

[![CI](https://github.com/marcus-qen/legator/actions/workflows/ci.yaml/badge.svg)](https://github.com/marcus-qen/legator/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/marcus-qen/legator)](https://goreportcard.com/report/github.com/marcus-qen/legator)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

**Autonomous agents that manage your entire IT estate — safely. Scheduled. Guardrailed. Audited.**

> **legator.io** · *"One who delegates."*

## What Is This?

Legator is a Kubernetes operator that runs autonomous LLM-powered agents. Install it on any cluster, define agents as CRDs, and they start monitoring, triaging, deploying, and remediating — automatically, safely, with a complete audit trail.

Agents run **on** Kubernetes but manage **anything reachable** — clusters, servers, databases, network devices, cloud resources, SaaS tools, legacy infrastructure. Kubernetes is the control plane; your entire IT estate is the target.

## Why Legator?

**No existing open-source tool combines all of these:**

- ⏰ **Scheduled execution** — cron, interval, webhook triggers with jitter and debounce
- 🛡️ **Runtime-enforced guardrails** — not prompt-advisory, actual Go code blocking dangerous actions
- 📋 **Immutable audit trail** — every action, decision, and block recorded as a Kubernetes resource
- 🔌 **Domain-agnostic** — same architecture for K8s, SSH, HTTP, SQL, DNS, MCP
- 📦 **K8s-native** — CRDs, RBAC, namespaces, Helm, GitOps-ready

Think "ArgoCD for autonomous agents" — declarative, reconciled, observable.

## Design Principles

1. **Lights-out operations** — no human in the loop for routine work
2. **Mutation-aware** — every action classified by risk before execution
3. **Functional, not just up** — agents verify real behaviour, not just pod status
4. **Data is sacred** — data mutations are NEVER automated, no override, no exception
5. **Audit everything** — immutable record of every action, decision, block, and escalation
6. **Portable** — same agent + same skills + different environment = different target

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                    legator-controller (Go binary)                     │
│                                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────────────┐ │
│  │ CRD Watcher  │  │  Scheduler  │  │       Agent Runner           │ │
│  │             │  │             │  │                              │ │
│  │ LegatorAgent│→ │  Cron       │→ │  1. Assemble prompt          │ │
│  │ Legator     │  │  Interval   │  │  2. Match Action Sheet       │ │
│  │   Environ.  │  │  Webhook    │  │  3. Call LLM                 │ │
│  │ ModelTier   │  │  Annotation │  │  4. Pre-flight check         │ │
│  └─────────────┘  └─────────────┘  │  5. Enforce guardrails       │ │
│                                     │  6. Execute or block         │ │
│                                     │  7. Record in LegatorRun     │ │
│                                     │  8. Report findings          │ │
│                                     └──────────────────────────────┘ │
│                                                                      │
│  ┌──────────────────────────────────────────────────────────────┐    │
│  │  Guardrail Engine                                            │    │
│  │  • Four-tier classification: read / service / destructive    │    │
│  │    / data-mutation                                           │    │
│  │  • Graduated autonomy: observe → recommend → automate → full │    │
│  │  • Protection classes: configurable per-domain safeguards    │    │
│  │  • Action Sheets: undeclared action = denied                 │    │
│  │  • Pre-flight checks, cooldowns, budget enforcement          │    │
│  └──────────────────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────────────────┘
         │              │              │              │
         ▼              ▼              ▼              ▼
    K8s API        MCP Servers      LLM APIs     Notification
    (kubectl)      (any tool)      (pluggable)    Channels
    SSH targets    HTTP APIs                      (Slack/TG)
    Databases      DNS servers
```

## CRDs

| CRD | Purpose | Scope |
|-----|---------|-------|
| **LegatorAgent** | Agent definition — identity, schedule, model tier, guardrails, skills | Namespaced |
| **LegatorEnvironment** | Site binding — endpoints, credentials, namespaces, data resources, tools | Namespaced |
| **ModelTierConfig** | Maps tier names (fast/standard/reasoning) → provider/model + auth | Cluster |
| **LegatorRun** | Immutable audit record — actions, pre-flight checks, blocks, escalations | Namespaced |

## Quick Start

```bash
# Install the operator
helm install legator oci://ghcr.io/marcus-qen/legator/charts/legator \
  --namespace legator-system --create-namespace

# Configure model access
kubectl apply -f examples/model-tier-config.yaml

# Deploy an environment binding
kubectl create namespace agents
kubectl apply -f examples/environments/dev-lab.yaml -n agents

# Deploy an agent
kubectl apply -f examples/agents/watchman-light.yaml -n agents

# Check status
kubectl get legatoragents -n agents
kubectl get legatorruns -n agents --sort-by=.metadata.creationTimestamp
```

## Safety Model

### Graduated Autonomy

| Level | Read | Recommend | Safe Mutations | Destructive Mutations |
|-------|------|-----------|----------------|----------------------|
| `observe` | ✅ | ❌ | ❌ | ❌ |
| `recommend` | ✅ | ✅ | ❌ | ❌ |
| `automate-safe` | ✅ | ✅ | ✅ | ❌ (escalate) |
| `automate-destructive` | ✅ | ✅ | ✅ | ✅ |

### Protection Classes

Configurable safeguards per domain. Kubernetes defaults ship built-in:

- **Never** delete PVCs, PVs, or change reclaim policies
- **Never** delete namespaces or database cluster CRs
- **Never** delete S3 objects or buckets

Protection classes extend to any domain — databases, APIs, filesystems, customer records. When a protected action is attempted, the runtime blocks it, logs the intent, and escalates.

### Action Sheets

Skills declare every action they can perform. The runtime enforces an **allowlist**: undeclared actions are denied. Each action includes its risk tier, pre-conditions, cooldown, and rollback strategy.

## Three-Layer Architecture

```
LegatorAgent (what / when / guardrails)
  + Skills (expertise — domain knowledge)
  + LegatorEnvironment (where — site-specific binding)
  = Autonomous, scheduled, guardrailed agent
```

- **LegatorAgent** is portable — deploy the same spec anywhere
- **Skills** are portable — domain-agnostic behavioural knowledge
- **LegatorEnvironment** is site-specific — credentials, endpoints, tool configuration

## Providers

Agents specify model needs as **tiers** (`fast`, `standard`, `reasoning`), not model names. The `ModelTierConfig` CRD maps tiers to providers:

```yaml
tiers:
  - tier: fast
    provider: anthropic
    model: claude-haiku-3-5-20241022
  - tier: standard
    provider: openai-compatible
    baseURL: https://api.moonshot.ai/v1
    model: kimi-latest
```

Supports: Anthropic, OpenAI, Ollama, vLLM, Kimi, any OpenAI-compatible endpoint.

## Telegram ChatOps (v0.9.0 P3.1 MVP)

Legator can run a Telegram-first ChatOps bot that routes commands through the existing API authz/safety gates (no direct Kubernetes bypass path).

### Supported MVP commands

- `/status`
- `/inventory [limit]`
- `/run <id>`
- `/approvals`
- `/approve <id> [reason]` (starts typed-confirmation flow)
- `/deny <id> [reason]` (starts typed-confirmation flow)
- `/confirm <id> <code>` (required to execute approve/deny)

### Required flags / env

- `--chatops-telegram-bot-token` (`CHATOPS_TELEGRAM_BOT_TOKEN`)
- `--chatops-telegram-bindings` (`CHATOPS_TELEGRAM_BINDINGS`)
- `--chatops-telegram-api-base-url` (`CHATOPS_TELEGRAM_API_BASE_URL`) — optional; defaults from `--api-bind-address`

Optional tuning:

- `--chatops-telegram-poll-interval` (default `2s`)
- `--chatops-telegram-long-poll-timeout` (default `25s`)
- `--chatops-telegram-confirmation-ttl` (default `2m`)

### Chat bindings format

`CHATOPS_TELEGRAM_BINDINGS` is a JSON array mapping Telegram chat IDs to API identities:

```json
[
  {
    "chatId": 7525575507,
    "subject": "telegram:7525575507",
    "email": "keith@example.com",
    "name": "Keith",
    "groups": ["legator-operator"]
  }
]
```

The mapped identity is evaluated by existing RBAC + UserPolicy rules, including `chat:use` and approval permissions.

## Documentation

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/getting-started.md) | Install → first agent in 10 minutes |
| [CRD Reference](docs/crd-reference.md) | Every field in every CRD |
| [Writing Skills](docs/skills-authoring.md) | Create custom agent expertise |
| [Environment Binding](docs/environment-binding.md) | Configure for your target |
| [Connectivity](docs/connectivity.md) | Headscale/Tailscale mesh VPN setup |
| [Data Protection](docs/data-protection.md) | How data resources are protected |
| [Guardrails Deep-Dive](docs/guardrails.md) | The complete safety model |
| [Model Tier Config](docs/model-tier-config.md) | Provider and model setup |
| [Troubleshooting](docs/troubleshooting.md) | Common issues and fixes |
| [Architecture Decisions](docs/adr/) | Why we made the choices we did |
| [Contributing](CONTRIBUTING.md) | Development setup and PR process |

## SSH Tool

Agents can SSH into servers to manage non-Kubernetes infrastructure:

```yaml
apiVersion: legator.io/v1alpha1
kind: LegatorAgent
metadata:
  name: legacy-server-scanner
spec:
  description: SSH into servers, parse configs, produce migration reports
  schedule:
    cron: "0 3 * * 1"
  guardrails:
    autonomy: observe
    allowedActions:
      - "ssh.exec read *"
  environmentRef: server-fleet
  skills:
    - name: server-migration-scan
      source: "configmap://skill-server-migration-scan"
```

**Built-in guardrails:**
- 🚫 Blocked commands: `dd`, `mkfs`, `fdisk`, `psql`, `mysql`, `mongo`, `redis-cli`
- 🔒 Protected paths: `/etc/shadow`, `/boot/`, `/dev/`, SSH keys
- 🔑 sudo: blocked by default, opt-in per host
- 👤 root login: blocked by default, opt-in per host
- 📏 Output truncation: 8KB max
- ⏱️ Per-command timeout: 30s default

Credentials are injected at the tool layer — the LLM never sees private keys or passwords.

## SQL Tool

Agents query databases for health monitoring, schema analysis, and performance auditing:

```yaml
apiVersion: legator.io/v1alpha1
kind: LegatorAgent
metadata:
  name: database-health-monitor
spec:
  description: Monitor PostgreSQL health metrics
  schedule:
    interval: "15m"
  guardrails:
    autonomy: observe
    allowedActions:
      - "sql.query read *"
  environmentRef: database-monitoring
  skills:
    - name: database-health
      source: "configmap://skill-database-health"
```

**Safety layers:**
- 🔒 Driver-level read-only: `sql.TxOptions{ReadOnly: true}`
- 🧮 Query classification: SELECT (read) / INSERT (data) / DROP (destructive) — anything non-read is blocked
- 🛡️ SQL injection detection: multi-statement, comment injection, suspicious UNION
- 📏 Result truncation: 1000 rows / 8KB max
- 🔑 Dynamic credentials: Vault creates ephemeral DB user per-run, auto-revoked on completion

Supports PostgreSQL (via pgx) and MySQL.

## Connectivity

Agents reach targets across network boundaries via **Headscale** (self-hosted WireGuard mesh):

```yaml
spec:
  connectivity:
    type: headscale
    headscale:
      controlServer: "https://headscale.example.com"
      authKeySecretRef: headscale-auth-key
      tags: ["tag:agent-runtime"]
      acceptRoutes: true
```

The Tailscale sidecar (optional Helm config) creates encrypted tunnels through NAT and firewalls. ACLs scope per-agent network access. Subnet routers expose entire VLANs without per-device installation.

See [Connectivity Guide](docs/connectivity.md) for architecture, ACLs, and deployment models.

## Vault Integration

Per-run dynamic credentials — nothing static, nothing permanent:

- **SSH**: Vault signs a 5-minute certificate. Target server trusts Vault CA. No static keys.
- **Database**: Vault creates a temporary DB user with specific grants. Auto-revoked.
- **KV**: Static secrets (API keys) stored in Vault, issued just-in-time.

Credentials are injected at the tool layer. The LLM never sees them.

## CLI

```bash
$ legator status
⚡ Legator Status
─────────────────
Agents:       10/10 ready
Environments: 1
Runs:         43 total (30 succeeded, 13 failed, 0 running)
Success rate: 69.8%
Tokens used:  293.4K

$ legator agents list
NAME            PHASE  AUTONOMY  SCHEDULE     RUNS  LAST RUN
watchman-light  Ready  observe   */5 * * * *  16    14s ago
forge           Ready  observe   */5 * * * *  16    1s ago
herald          Ready  observe   0 8 * * *    1     1h ago
...

$ legator init
🚀 Legator Agent Init
=====================
  Agent name [my-agent]: cert-monitor
  Description [Autonomous agent...]: Monitor TLS certificate expiry
  Namespace [agents]: agents
  Autonomy level [observe]: observe
  Schedule (cron) [0 * * * *]: 0 8 * * *
  Model tier [standard]: fast
  Primary tool domain [kubernetes]: kubernetes
  Starter skill [cluster-health]: certificate-expiry

Creating agent in ./cert-monitor ...
  📄 cert-monitor/agent.yaml
  📄 cert-monitor/environment.yaml
  📄 cert-monitor/skill/SKILL.md
  📄 cert-monitor/skill/actions.yaml

✅ Agent scaffold created!

$ legator validate cert-monitor/
🔍 Validating agent in cert-monitor/ ...

  ✅ agent.yaml is valid YAML
  ✅ agent name: cert-monitor
  ✅ autonomy: observe
  ✅ schedule: 0 8 * * *
  ✅ 3 tool(s) allowed
  ✅ skill source: configmap://cert-monitor-skill
  ✅ environment.yaml is valid YAML
  ✅ skill/SKILL.md (612 bytes)
  ✅ skill/actions.yaml: 3 action(s) defined

✅ Validation passed — agent is ready to deploy

$ legator runs logs forge-ckmpm
Run: forge-ckmpm
Agent: forge | Phase: Succeeded | Trigger: scheduled
Model: openai/kimi-latest
Iterations: 4 | Tokens: 9830 | Duration: 39.3s
Autonomy: observe | Budget: 4/10 iterations, 50000 token budget

Actions (9):
  1. ✅ kubectl.get applications -n argocd
  2. ✅ kubectl.get freight -n backstage
  ...
```

## Examples

| Example | Description |
|---------|-------------|
| [hello-world](examples/agents/hello-world.yaml) | Simplest possible agent |
| [watchman-light](examples/agents/watchman-light.yaml) | Endpoint monitoring (5-min cron) |
| [legacy-server-scanner](examples/agents/legacy-server-scanner.yaml) | SSH server migration scan |
| [patch-compliance-checker](examples/agents/patch-compliance-checker.yaml) | SSH fleet patch audit |
| [database-health-monitor](examples/agents/database-health-monitor/) | PostgreSQL health monitoring |
| [schema-drift-detector](examples/agents/schema-drift-detector/) | Cross-environment schema diff |
| [server-health-monitor](examples/agents/server-health-monitor/) | SSH health checks via Headscale |
| [multi-cluster](examples/agents/multi-cluster-watchman.yaml) | Monitor a remote cluster |
| [All agents](examples/agents/) | Full ops team + SSH + SQL examples |

## Web Dashboard

Legator ships with a web dashboard for observability:

- **Agent overview** — status, autonomy level, schedule, last run
- **Run history** — success/failure, token usage, duration, full audit trail
- **Approval queue** — pending approval requests, approve/deny from CLI or dashboard
- **Event feed** — inter-agent events, severity, consumer tracking

Deploy via Helm: `dashboard.enabled: true`. OIDC auth ready for Keycloak hookup.

## Production Status

**v0.6.0** — Running on a 4-node Talos Kubernetes cluster. 10 autonomous agents managing platform operations as sole operator. Eight tool domains (kubectl, HTTP, SSH, SQL, DNS, state, AWS, Azure) plus A2A task delegation. OIDC authentication (Keycloak), real OCI registry push/pull (ORAS), multi-tenant quotas, notification channels, agent-to-agent collaboration. 454 tests across 28 packages.

### Dogfooding Fleet

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

**Observability**: 9 Prometheus metrics, Grafana dashboard, Tempo traces, 4 alert rules.

### What We Learned

- **Token budgets matter** — agents without budgets waste 3-5× more tokens
- **Credential injection must override LLM headers** — LLMs read placeholders and set them as literal values
- **Force-report on final iteration** — without it, agents gather data forever and never synthesise
- **Conversation pruning prevents quadratic growth** — sliding window keeps token usage flat

## Roadmap

| Version | Focus |
|---------|-------|
| **v0.1.0** ✅ | Core operator, 10 K8s agents, dogfooding |
| **v0.2.0** ✅ | Rename to Legator, protection classes, SSH tool, CLI |
| **v0.3.0** ✅ | Vault integration, SQL tool, Headscale connectivity |
| **v0.4.0** | Web dashboard, DNS tool, A2A protocol, multi-tenant |

## License

Apache License 2.0

---

*legator (n.): one who delegates. From Latin lēgāre, "to send as ambassador, to commission."*
