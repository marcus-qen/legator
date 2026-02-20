# InfraAgent v0.1.0 — Release Notes

**First release.** Kubernetes operator for autonomous infrastructure agents — lights-out cluster management with hard safety guarantees.

## What Is It?

`helm install infraagent` on any Kubernetes cluster. Apply CRDs defining your agents — what they do, when they run, what they're allowed to touch. Agents start monitoring, triaging, verifying, and reporting automatically. Every action is logged in an immutable audit trail. Data mutations are blocked unconditionally.

## Highlights

### Safety First
- **Four-tier action classification**: read → service-mutation → destructive-mutation → data-mutation
- **Graduated autonomy**: observe → recommend → automate-safe → automate-destructive
- **Data protection is non-configurable**: PVC/PV/namespace/database deletion blocked in code. No flag can override.
- **Action Sheets**: Skills declare every action upfront. Undeclared = denied.
- **Credential sanitization**: Secrets never appear in AgentRun audit trails.

### Three-Layer Portability
- **InfraAgent** (what/when/guardrails) — portable across clusters
- **Skills** (expertise) — infrastructure-agnostic behavioural knowledge
- **AgentEnvironment** (site binding) — the only thing that changes per cluster

### Provider Flexibility
- Supports Anthropic, OpenAI, and any OpenAI-compatible endpoint (Kimi, Ollama, vLLM, etc.)
- `ModelTierConfig` CRD maps `fast`/`standard`/`reasoning` tiers to actual models
- Switched providers during dogfooding (Anthropic → Kimi) with zero code changes

### Production Features
- Cron, interval, and webhook scheduling with jitter and concurrency limits
- Conversation pruning (sliding window) prevents quadratic token growth
- Force-report on final iteration ensures agents always produce output
- Per-call token caps (8192) prevent single-response budget exhaustion
- AgentRun retention with TTL + per-agent preserve-min
- Leader election, graceful shutdown, rate limiting

### Observability
- 9 Prometheus metrics (runs, duration, tokens, iterations, findings, escalations, schedule lag, active runs, guardrail blocks)
- OpenTelemetry tracing with GenAI semantic conventions
- Grafana dashboard (auto-provisioned via sidecar)
- 4 alert rules (high failure rate, stale schedule, token burn, zero success)

### Integrations
- MCP client for tool servers (k8sgpt, etc.)
- Git and ConfigMap skill loading
- Multi-cluster support (remote kubeconfig)
- HTTP credential auto-injection (keeps secrets out of LLM context)

## Dogfooding Results

Running on a 4-node Talos Kubernetes cluster with 10 autonomous agents:

- **275+ AgentRuns** over 12+ hours of observation
- **10/10 agents succeeding** (monitoring, deployment verification, issue triage, config auditing, E2E verification, daily briefings)
- **82% overall success rate** (100% for recent runs — early failures were from configuration/budget tuning)
- **Token efficiency**: 3K–20K tokens per run depending on agent complexity
- **Schedule accuracy**: <7s average lag

## Install

```bash
# Helm chart
helm install infraagent oci://ghcr.io/marcus-qen/infraagent/charts/infraagent \
  --version 0.1.0 \
  --namespace infraagent-system --create-namespace

# Container image
docker pull ghcr.io/marcus-qen/infraagent:v0.1.0
```

## What's Next (v0.2.0)

- A2A protocol for agent-to-agent communication
- Persistent agent state (cross-run memory)
- OCI-based skill distribution
- Admission webhook for CRD validation
- Multi-tenant isolation

## 253 tests. Apache 2.0. Built in Go.

---

*"We are what we repeatedly do. Excellence, then, is not an act, but a habit." — Aristotle (via Durant)*
