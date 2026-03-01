# Legator

**A self-hosted fleet management control plane that deploys AI probes across heterogeneous infrastructure.**

Conversational ops. Policy guardrails. Full audit trail. No vendor lock-in.

> "Zabbix meets ChatGPT with the policy engine of a bank."

## What is this?

Legator is a single control-plane application that:

- **Deploys probes** onto servers, VMs, containers, Kubernetes nodes, and cloud accounts
- **Shows you everything** — health, inventory, activity, risk per host
- **Lets you talk to probes** in persistent two-way chat ("restart nginx on web-03")
- **Enforces guardrails** — read-only by default, graduated autonomy, human approval for risky changes
- **Audits every action** with before/after state and who approved what

The LLM never touches your servers directly. The probe never reasons independently. **Brain and hands stay separate.**

## Architecture

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                               Control Plane                                │
│  Web UI · REST API · WebSocket Hub · Policy Engine · Audit Log             │
│  Model Dock (BYOK profiles + routing + usage) · Alerts Engine              │
└──────────────────────┬───────────────────────────────┬──────────────────────┘
                       │                               │
                  WSS/TLS                          Cloud APIs
                       │                         (agentless ingest)
            ┌──────────┼──────────┐             ┌──────┬──────┬──────┐
            ▼          ▼          ▼             ▼      ▼      ▼      
        ┌───────┐  ┌───────┐  ┌───────┐      AWS    GCP    Azure
        │ Probe │  │ Probe │  │ Probe │
        │ web-01│  │ db-01 │  │ k8s-03│
        └───────┘  └───────┘  └───────┘
```

- **Control plane**: standalone Go binary (14MB), runs anywhere
- **Probe**: static Go binary (7MB), zero deps, runs as systemd, DaemonSet, or Windows service
- **legatorctl**: CLI for fleet operations
- **Connection**: persistent WebSocket, heartbeat every 30s, reconnect with jitter

See [docs/architecture.md](docs/architecture.md) for full internals.

Architecture boundary/ownership CI contract: [docs/contracts/architecture-boundaries.yaml](docs/contracts/architecture-boundaries.yaml) (guide: [docs/architecture/ci-boundary-guardrails.md](docs/architecture/ci-boundary-guardrails.md)).
Stage 3.6.3 baseline lock artifact: [docs/contracts/architecture-cross-boundary-imports.txt](docs/contracts/architecture-cross-boundary-imports.txt).
Stage 3.6.4 exception registry: [docs/contracts/architecture-boundary-exceptions.yaml](docs/contracts/architecture-boundary-exceptions.yaml).
Contributor workflow: [CONTRIBUTING.md](CONTRIBUTING.md).

## Quick Start

### 1. Build

```bash
git clone https://github.com/marcus-qen/legator.git
cd legator
make build
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

### 3. Connect probes

```bash
# Generate a registration token
TOKEN=$(curl -sf -X POST http://localhost:8080/api/v1/tokens | jq -r .token)

# Linux one-liner install
curl -sSL http://localhost:8080/install.sh | sudo bash -s -- \
  --server http://localhost:8080 --token "$TOKEN"

# Manual (development)
./bin/probe init --server http://localhost:8080 --token "$TOKEN"
./bin/probe run
```

**Kubernetes DaemonSet (auto-init):**

```bash
kubectl create ns legator
kubectl -n legator create secret generic legator-probe \
  --from-literal=LEGATOR_SERVER_URL=https://legator.example.com \
  --from-literal=LEGATOR_TOKEN=<multi-use-token> \
  --from-literal=LEGATOR_TAGS=k8s,prod

kubectl -n legator apply -f deploy/k8s/probe-daemonset.yaml
```

**Windows (PowerShell, Admin):**

```powershell
$env:LEGATOR_SERVER_URL = "https://legator.example.com"
$env:LEGATOR_TOKEN = "<token>"
$env:LEGATOR_TAGS = "windows,prod"
.\probe.exe init
.\probe.exe service install
```

### 4. See your fleet

Open `http://localhost:8080/` in a browser, or:

```bash
curl -sf http://localhost:8080/api/v1/fleet/summary | jq
```

📖 **Full guide:** [docs/getting-started.md](docs/getting-started.md)
📖 **Kubeflow adapter:** [docs/kubeflow-adapter.md](docs/kubeflow-adapter.md)
📖 **Grafana adapter:** [docs/grafana-adapter.md](docs/grafana-adapter.md)
📖 **Federation read model:** [docs/federation-read-model.md](docs/federation-read-model.md)

## Features

| Feature | Status |
|---|---|
| Fleet view (web UI) | ✅ |
| Fleet page redesign (3-panel master-detail + 5 detail tabs + embedded chat) | ✅ |
| Per-probe chat (LLM-powered) | ✅ |
| Fleet-level chat (multi-probe context + targeted dispatch) | ✅ |
| Policy engine (observe/diagnose/remediate) | ✅ |
| Defence-in-depth policy enforcement | ✅ |
| Approval queue with risk classification | ✅ |
| Immutable audit log | ✅ |
| Alert rules engine + alerts dashboard | ✅ |
| HMAC-SHA256 command signing | ✅ |
| Output streaming (SSE) | ✅ |
| Probe self-update | ✅ |
| K8s DaemonSet probe deployment (auto-init + multi-use tokens + K8s inventory enrichment) | ✅ |
| Windows probe support (MVP) | ✅ |
| Cloud connectors (AWS/GCP/Azure, agentless inventory ingestion) | ✅ |
| Kubeflow adapter MVP (read-only status/inventory + guarded refresh action) | ✅ |
| Grafana adapter Stage 2.1 (read-only status + capacity snapshot) | ✅ |
| Federation read model Stage 3.7.3 (scoped auth + tenancy boundaries across API/MCP/UI) | ✅ |
| Capacity-aware policy decisions Stage 2.2 (allow/deny/queue + rationale payloads) | ✅ |
| Operator explainability panel Stage 2.3 (approval UI rationale + capacity drivers) | ✅ |
| Auto-discovery + registration assist (network/SSH scan + guided registration) | ✅ |
| BYOK model dock (multi-vendor key profiles + runtime model switching + usage tracking) | ✅ |
| Tags + group commands | ✅ |
| Multi-user RBAC | ✅ |
| OIDC/SSO authentication | ✅ |
| Consistent JSON error API | ✅ |
| Dark UI with centered chat | ✅ |
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
| `LEGATOR_OIDC_ENABLED` | `false` | Enable OIDC authentication |
| `LEGATOR_OIDC_PROVIDER_URL` | — | OIDC provider URL (e.g. Keycloak realm) |
| `LEGATOR_OIDC_CLIENT_ID` | — | OIDC client ID |
| `LEGATOR_OIDC_CLIENT_SECRET` | — | OIDC client secret |
| `LEGATOR_OIDC_REDIRECT_URL` | — | OIDC callback URL |
| `LEGATOR_LLM_PROVIDER` | — | LLM provider (e.g. `openai`) |
| `LEGATOR_LLM_BASE_URL` | — | LLM API base URL |
| `LEGATOR_LLM_API_KEY` | — | LLM API key |
| `LEGATOR_LLM_MODEL` | — | LLM model name |
| `LEGATOR_KUBEFLOW_ENABLED` | `false` | Enable Kubeflow adapter endpoints |
| `LEGATOR_KUBEFLOW_NAMESPACE` | `kubeflow` | Namespace used for Kubeflow inventory |
| `LEGATOR_KUBEFLOW_KUBECONFIG` | — | Optional kubeconfig path for kubectl |
| `LEGATOR_KUBEFLOW_CONTEXT` | — | Optional kubeconfig context override |
| `LEGATOR_KUBEFLOW_TIMEOUT` | `15s` | Timeout per kubectl call for adapter reads |
| `LEGATOR_KUBEFLOW_ACTIONS_ENABLED` | `false` | Enable guarded Kubeflow mutation endpoints (`POST /api/v1/kubeflow/actions/refresh`, `POST /api/v1/kubeflow/runs/submit`, `POST /api/v1/kubeflow/runs/{name}/cancel`) |
| `LEGATOR_GRAFANA_ENABLED` | `false` | Enable Grafana adapter endpoints |
| `LEGATOR_GRAFANA_BASE_URL` | — | Grafana base URL for adapter reads |
| `LEGATOR_GRAFANA_API_TOKEN` | — | Optional Bearer token for Grafana API reads |
| `LEGATOR_GRAFANA_TIMEOUT` | `10s` | Timeout per Grafana adapter API call |
| `LEGATOR_GRAFANA_DASHBOARD_LIMIT` | `10` | Max dashboards scanned per snapshot (capped at 100) |
| `LEGATOR_GRAFANA_TLS_SKIP_VERIFY` | `false` | Skip TLS verify for self-signed Grafana certs |
| `LEGATOR_GRAFANA_ORG_ID` | `0` | Optional Grafana organization ID header |
| `LEGATOR_JOBS_RETRY_MAX_ATTEMPTS` | `1` | Global default max attempts for scheduled-job retries (includes first attempt) |
| `LEGATOR_JOBS_RETRY_INITIAL_BACKOFF` | `5s` | Global default initial retry delay for scheduled-job retries |
| `LEGATOR_JOBS_RETRY_MULTIPLIER` | `2` | Global default exponential multiplier for scheduled-job retries |
| `LEGATOR_JOBS_RETRY_MAX_BACKOFF` | none | Optional global cap for retry delay progression |
| `LEGATOR_SERVER_URL` | — | Probe auto-init: control plane URL |
| `LEGATOR_TOKEN` | — | Probe auto-init: registration token |
| `LEGATOR_TAGS` | — | Probe auto-init: comma-separated tags |
| `LEGATOR_HOSTNAME` | host OS name | Probe hostname override |

## Building

```bash
make build            # Build control-plane, probe, legatorctl
make architecture-guard # Fast-fail architecture guardrails only
make preflight        # architecture-guard + go test ./...
make test             # Run unit tests
make e2e              # Full end-to-end flow (29+ checks)
make lint             # golangci-lint
make release-build    # Cross-compile release binaries (incl. windows/amd64 probe)
```

## API

Compatibility/deprecation policy: `docs/api-mcp-compatibility.md`.

50+ REST endpoints. Key groups:

- **Fleet**: `GET /api/v1/probes`, `GET /api/v1/fleet/summary`, `POST /api/v1/probes/{id}/command`
- **Federation (read-only)**: `GET /api/v1/federation/inventory`, `GET /api/v1/federation/summary`
  - Additive tenancy filters: `tenant_id` (`tenant`), `org_id` (`org`), `scope_id` (`scope`)
  - Optional scoped auth grants on API keys/users: `tenant:<id>`, `org:<id>`, `scope:<id>` (restricts returned federation data even when no explicit tenancy query is provided)
  - Additive consistency/failover indicators on source + rollup payloads:
    - source `consistency`: `freshness`, `completeness`, `degraded`, `failover_mode`, `snapshot_age_seconds`
    - top-level `consistency`: `freshness`, `completeness`, `partial_results`, `failover_active`, source consistency counters
- **Jobs**: `GET/POST /api/v1/jobs`, `POST /api/v1/jobs/{id}/run`, `POST /api/v1/jobs/{id}/cancel`, `GET /api/v1/jobs/{id}/runs`, `POST /api/v1/jobs/{id}/runs/{runId}/cancel`, `POST /api/v1/jobs/{id}/runs/{runId}/retry`, `GET /api/v1/jobs/runs`
  - Optional per-job retry policy (additive):
    - `retry_policy.max_attempts`
    - `retry_policy.initial_backoff` (duration, e.g. `10s`)
    - `retry_policy.multiplier` (exponential factor, e.g. `2`)
    - `retry_policy.max_backoff` (optional cap duration)
  - Run history now includes correlation metadata: `execution_id`, `attempt`, `max_attempts`, `retry_scheduled_at`.
  - Capacity-aware admission outcomes are additive in run payloads: `admission_decision`, `admission_reason`, `admission_rationale`.
  - Run status filters include: `queued`, `pending`, `running`, `success`, `failed`, `canceled`, `denied`.
- **Chat**: `GET/POST /api/v1/probes/{id}/chat`, `GET /ws/chat`
- **Fleet Chat**: `GET/POST /api/v1/fleet/chat`
- **Policy**: `GET/POST /api/v1/policies`, `POST /api/v1/probes/{id}/apply-policy/{policyId}`
- **Approvals**: `GET /api/v1/approvals`, `POST /api/v1/approvals/{id}/decide`
  - Pending approvals now carry additive explainability fields when available:
    - `policy_decision`
    - `policy_rationale` (includes `summary`, `thresholds`, `capacity`, `indicators[]`, `drove_outcome`)
- **Audit**: `GET /api/v1/audit`
- **Alerts**: `GET/POST /api/v1/alerts`, `GET/PUT/DELETE /api/v1/alerts/{id}`, `GET /api/v1/alerts/{id}/history`, `GET /api/v1/alerts/active`
- **Webhooks**: `GET/POST /api/v1/webhooks`
- **Auth**: `GET/POST/DELETE /api/v1/auth/keys`, `GET/POST/DELETE /api/v1/users`
- **Model Dock**: `GET/POST /api/v1/model-profiles`, `PUT/DELETE /api/v1/model-profiles/{id}`, `POST /api/v1/model-profiles/{id}/activate`, `GET /api/v1/model-profiles/active`, `GET /api/v1/model-usage`
- **Cloud Connectors**: `GET/POST /api/v1/cloud/connectors`, `PUT/DELETE /api/v1/cloud/connectors/{id}`, `POST /api/v1/cloud/connectors/{id}/scan`, `GET /api/v1/cloud/assets`
- **Kubeflow**: `GET /api/v1/kubeflow/status`, `GET /api/v1/kubeflow/inventory`, `GET /api/v1/kubeflow/runs/{name}/status`, `POST /api/v1/kubeflow/actions/refresh`, `POST /api/v1/kubeflow/runs/submit`, `POST /api/v1/kubeflow/runs/{name}/cancel` (mutations disabled by default)
- **Grafana**: `GET /api/v1/grafana/status`, `GET /api/v1/grafana/snapshot` (disabled by default)
- **Network Devices**: `GET/POST /api/v1/network/devices`, `GET/PUT/DELETE /api/v1/network/devices/{id}`, `POST /api/v1/network/devices/{id}/test`, `POST /api/v1/network/devices/{id}/inventory`
- **Discovery**: `POST /api/v1/discovery/scan`, `GET /api/v1/discovery/runs`, `GET /api/v1/discovery/runs/{id}`, `POST /api/v1/discovery/install-token`
- **Metrics**: `GET /api/v1/metrics`
- **Events**: `GET /api/v1/events` (SSE stream)
  - Job lifecycle events are emitted to both audit and SSE/event bus surfaces:
    - `job.created`, `job.updated`, `job.deleted`
    - `job.run.admission_allowed`, `job.run.admission_queued`, `job.run.admission_denied`
    - `job.run.queued`, `job.run.started`, `job.run.retry_scheduled`
    - `job.run.succeeded`, `job.run.failed`, `job.run.canceled`, `job.run.denied`
  - Job run events carry correlation metadata where available: `job_id`, `run_id`, `execution_id`, `probe_id`, `attempt`, `max_attempts`, `request_id`, `admission_decision`, `admission_reason`, `admission_rationale`.

## Status

**v1.0.0-alpha.10** — 155 Go files, 30 test suites, 29/29 e2e.

## License

[Apache 2.0](LICENSE)

---

*"One who delegates." — Legator*
