# Getting Started with Legator

This gets you from zero to a working control plane with connected probes in ~15 minutes.

## Prerequisites

- Go 1.24+ (build from source)
- Linux, Kubernetes, or Windows target hosts (amd64)
- LLM API key (optional, needed for chat/task features)

## 1) Build

```bash
git clone https://github.com/marcus-qen/legator.git
cd legator
make build
```

Binaries in `bin/`:
- `control-plane` (14MB)
- `probe` (7MB)
- `legatorctl` (6MB)

## 2) Start the control plane

### Minimal (dev)

```bash
./bin/control-plane
```

### Production

```bash
export LEGATOR_LISTEN_ADDR=":9080"
export LEGATOR_DATA_DIR="/var/lib/legator"
export LEGATOR_SIGNING_KEY="$(openssl rand -hex 32)"
export LEGATOR_AUTH=true

# Optional LLM setup
export LEGATOR_LLM_PROVIDER=openai
export LEGATOR_LLM_BASE_URL=https://api.openai.com/v1
export LEGATOR_LLM_API_KEY=sk-...
export LEGATOR_LLM_MODEL=gpt-4o-mini

./bin/control-plane
```

When `LEGATOR_AUTH=true`, first startup prints an admin password to stderr. Save it.

## 3) Create registration token(s)

```bash
curl -sf -X POST http://localhost:8080/api/v1/tokens | jq
```

Default tokens are short-lived and single-use. For automation (DaemonSet, bootstrap jobs), use install-token/discovery flows to mint multi-use install tokens.

## 4) Connect probes

### Linux one-liner (production)

```bash
curl -sSL https://your-server/install.sh | sudo bash -s -- \
  --server https://your-server:8080 \
  --token <token>
```

### Linux manual (dev/testing)

```bash
./bin/probe init --server http://localhost:8080 --token <token>
./bin/probe service install
# or foreground mode
./bin/probe run
```

### Kubernetes DaemonSet deployment

Use this when you want every node covered automatically.

```bash
kubectl create ns legator
kubectl -n legator create secret generic legator-probe \
  --from-literal=LEGATOR_SERVER_URL=https://legator.example.com \
  --from-literal=LEGATOR_TOKEN=<multi-use-token> \
  --from-literal=LEGATOR_TAGS=k8s,prod

kubectl -n legator apply -f deploy/k8s/probe-daemonset.yaml
```

Probe auto-init env vars used by the DaemonSet:

| Variable | Purpose |
|---|---|
| `LEGATOR_SERVER_URL` | Control plane URL |
| `LEGATOR_TOKEN` | Registration token (single or multi-use) |
| `LEGATOR_TAGS` | Comma-separated tags |
| `LEGATOR_HOSTNAME` | Optional hostname override |

### Windows probe setup (PowerShell, Administrator)

```powershell
$env:LEGATOR_SERVER_URL = "https://legator.example.com"
$env:LEGATOR_TOKEN = "<token>"
$env:LEGATOR_TAGS = "windows,prod"
# optional
$env:LEGATOR_HOSTNAME = "win-sql-01"

.\probe.exe init
.\probe.exe service install
```

Windows defaults:
- config: `%ProgramData%\Legator\probe-config\config.yaml`
- data/logs: `%ProgramData%\Legator\`

## 5) Verify fleet connectivity

Open `http://localhost:8080/` and check Fleet.

Or via API:

```bash
curl -sf http://localhost:8080/api/v1/probes | jq
curl -sf http://localhost:8080/api/v1/fleet/summary | jq
```

## 6) Cloud connector configuration (agentless ingest)

Cloud connectors pull inventory directly from provider APIs (AWS/GCP/Azure), no probe needed for basic asset visibility.

```bash
# Create connector
curl -sf -X POST http://localhost:8080/api/v1/cloud/connectors \
  -H Content-Type: application/json \
  -d provider:aws | jq

# Trigger scan
curl -sf -X POST http://localhost:8080/api/v1/cloud/connectors/<id>/scan | jq

# List discovered assets
curl -sf http://localhost:8080/api/v1/cloud/assets | jq
```

Start small. One account/project/subscription first. Expand once scan latency and permissions look sane.

## 7) Model dock setup (BYOK)

Model Dock lets you keep your own vendor keys, switch active models at runtime, and track usage.

```bash
# Add model profile
curl -sf -X POST http://localhost:8080/api/v1/model-profiles \
  -H Content-Type: application/json \
  -d name:aws-prod | jq

# Trigger scan
curl -sf -X POST http://localhost:8080/api/v1/cloud/connectors/<id>/scan | jq

# List discovered assets
curl -sf http://localhost:8080/api/v1/cloud/assets | jq
```

Start small. One account/project/subscription first. Expand once scan latency and permissions look sane.

## 7) Model dock setup (BYOK)

Model Dock lets you keep your own vendor keys, switch active models at runtime, and track usage.

```bash
# Add model profile
curl -sf -X POST http://localhost:8080/api/v1/model-profiles \
  -H Content-Type: application/json \
  -d config:{region:eu-west-2} | jq

# Trigger scan
curl -sf -X POST http://localhost:8080/api/v1/cloud/connectors/<id>/scan | jq

# List discovered assets
curl -sf http://localhost:8080/api/v1/cloud/assets | jq
```

Start small. One account/project/subscription first. Expand once scan latency and permissions look sane.

## 7) Model dock setup (BYOK)

Model Dock lets you keep your own vendor keys, switch active models at runtime, and track usage.

```bash
# Add model profile
curl -sf -X POST http://localhost:8080/api/v1/model-profiles \
  -H Content-Type: application/json \
  -d name:openai-prod | jq

# Activate profile
curl -sf -X POST http://localhost:8080/api/v1/model-profiles/<id>/activate | jq

# Check active profile + usage
curl -sf http://localhost:8080/api/v1/model-profiles/active | jq
curl -sf http://localhost:8080/api/v1/model-usage | jq
```

Rule of thumb: separate profiles by environment (dev/staging/prod) and vendor. Do not share one key across everything.

## Policy levels (guardrails)

| Level | Can do | Examples |
|---|---|---|
| **observe** | Read-only operations | `ls`, `cat`, `ps`, `df`, `uptime` |
| **diagnose** | observe + analysis/debug | `strace`, `tcpdump`, `dig`, `ping` |
| **remediate** | diagnose + writes/changes | `rm`, `apt install`, `systemctl restart` |

Default is **observe**. Keep it there unless you have a reason.

## Next steps

- Set up alerts via `GET/POST /api/v1/alerts`
- Use fleet chat via `GET/POST /api/v1/fleet/chat`
- Configure SSO (`LEGATOR_OIDC_*`) for multi-user access
- Wire metrics (`GET /api/v1/metrics`) into Prometheus/Grafana

---

*Built with Go. Runs anywhere. No nonsense.*
EOF application/json \
  -d provider:openai | jq

# Activate profile
curl -sf -X POST http://localhost:8080/api/v1/model-profiles/<id>/activate | jq

# Check active profile + usage
curl -sf http://localhost:8080/api/v1/model-profiles/active | jq
curl -sf http://localhost:8080/api/v1/model-usage | jq
```

Rule of thumb: separate profiles by environment (dev/staging/prod) and vendor. Do not share one key across everything.

## Policy levels (guardrails)

| Level | Can do | Examples |
|---|---|---|
| **observe** | Read-only operations | `ls`, `cat`, `ps`, `df`, `uptime` |
| **diagnose** | observe + analysis/debug | `strace`, `tcpdump`, `dig`, `ping` |
| **remediate** | diagnose + writes/changes | `rm`, `apt install`, `systemctl restart` |

Default is **observe**. Keep it there unless you have a reason.

## Next steps

- Set up alerts via `GET/POST /api/v1/alerts`
- Use fleet chat via `GET/POST /api/v1/fleet/chat`
- Configure SSO (`LEGATOR_OIDC_*`) for multi-user access
- Wire metrics (`GET /api/v1/metrics`) into Prometheus/Grafana

---

*Built with Go. Runs anywhere. No nonsense.*
EOF application/json \
  -d model:gpt-4.1 | jq

# Activate profile
curl -sf -X POST http://localhost:8080/api/v1/model-profiles/<id>/activate | jq

# Check active profile + usage
curl -sf http://localhost:8080/api/v1/model-profiles/active | jq
curl -sf http://localhost:8080/api/v1/model-usage | jq
```

Rule of thumb: separate profiles by environment (dev/staging/prod) and vendor. Do not share one key across everything.

## Policy levels (guardrails)

| Level | Can do | Examples |
|---|---|---|
| **observe** | Read-only operations | `ls`, `cat`, `ps`, `df`, `uptime` |
| **diagnose** | observe + analysis/debug | `strace`, `tcpdump`, `dig`, `ping` |
| **remediate** | diagnose + writes/changes | `rm`, `apt install`, `systemctl restart` |

Default is **observe**. Keep it there unless you have a reason.

## Next steps

- Set up alerts via `GET/POST /api/v1/alerts`
- Use fleet chat via `GET/POST /api/v1/fleet/chat`
- Configure SSO (`LEGATOR_OIDC_*`) for multi-user access
- Wire metrics (`GET /api/v1/metrics`) into Prometheus/Grafana

---

*Built with Go. Runs anywhere. No nonsense.*
EOF application/json \
  -d api_key:sk-... | jq

# Activate profile
curl -sf -X POST http://localhost:8080/api/v1/model-profiles/<id>/activate | jq

# Check active profile + usage
curl -sf http://localhost:8080/api/v1/model-profiles/active | jq
curl -sf http://localhost:8080/api/v1/model-usage | jq
```

Rule of thumb: separate profiles by environment (dev/staging/prod) and vendor. Do not share one key across everything.

## Policy levels (guardrails)

| Level | Can do | Examples |
|---|---|---|
| **observe** | Read-only operations | `ls`, `cat`, `ps`, `df`, `uptime` |
| **diagnose** | observe + analysis/debug | `strace`, `tcpdump`, `dig`, `ping` |
| **remediate** | diagnose + writes/changes | `rm`, `apt install`, `systemctl restart` |

Default is **observe**. Keep it there unless you have a reason.

## Next steps

- Set up alerts via `GET/POST /api/v1/alerts`
- Use fleet chat via `GET/POST /api/v1/fleet/chat`
- Configure SSO (`LEGATOR_OIDC_*`) for multi-user access
- Wire metrics (`GET /api/v1/metrics`) into Prometheus/Grafana

---

*Built with Go. Runs anywhere. No nonsense.*
