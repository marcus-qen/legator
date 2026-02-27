# Getting Started with Legator

This guide takes you from zero to a working control plane with a connected probe in under 15 minutes.

## Prerequisites

- Go 1.24+ (for building from source)
- Linux or Windows host for the probe (amd64)
- An LLM API key (optional — needed for chat/task features)

## Step 1: Build

```bash
git clone https://github.com/marcus-qen/legator.git
cd legator
make all
```

This produces three binaries in `bin/`:
- `control-plane` (14MB) — the central server
- `probe` (7MB) — the agent that runs on target machines
- `legatorctl` (6MB) — CLI for fleet management

## Step 2: Start the Control Plane

### Minimal (development)

```bash
./bin/control-plane
```

This starts on `:8080` with in-memory state, auto-generated signing key, and no authentication.

### Production

```bash
export LEGATOR_LISTEN_ADDR=":9080"
export LEGATOR_DATA_DIR="/var/lib/legator"
export LEGATOR_SIGNING_KEY="$(openssl rand -hex 32)"
export LEGATOR_AUTH=true

# Optional: LLM for chat and task features
export LEGATOR_LLM_PROVIDER=openai
export LEGATOR_LLM_BASE_URL=https://api.openai.com/v1
export LEGATOR_LLM_API_KEY=sk-...
export LEGATOR_LLM_MODEL=gpt-4o-mini

./bin/control-plane
```

**Configuration options:**

| Variable | Default | Description |
|---|---|---|
| `LEGATOR_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `LEGATOR_DATA_DIR` | `/var/lib/legator` | SQLite databases location |
| `LEGATOR_SIGNING_KEY` | auto-generated | HMAC key for command signing (hex, 64+ chars) |
| `LEGATOR_AUTH` | `false` | Enable authentication (API keys + sessions) |
| `LEGATOR_LLM_PROVIDER` | — | LLM provider name |
| `LEGATOR_LLM_BASE_URL` | — | LLM API base URL |
| `LEGATOR_LLM_API_KEY` | — | LLM API key |
| `LEGATOR_LLM_MODEL` | — | LLM model name |
| `LEGATOR_TASK_APPROVAL_WAIT` | `2m` | How long to wait for approval before timeout |

When `LEGATOR_AUTH=true`, the first startup prints an admin password to stderr. Save it.

## Step 3: Generate a Registration Token

```bash
curl -sf -X POST http://localhost:8080/api/v1/tokens | jq
```

Response:
```json
{
  "token": "prb_a8f3d1e9c2b4_1740000000_hmac_7f2a",
  "created": "2026-02-26T12:00:00Z",
  "expires": "2026-02-26T12:30:00Z"
}
```

Tokens are single-use and expire after 30 minutes.

## Step 4: Connect a Probe

### Option A: One-liner install (recommended for production)

```bash
curl -sSL https://your-server/install.sh | sudo bash -s -- \
  --server https://your-server:8080 \
  --token prb_a8f3d1e9c2b4_1740000000_hmac_7f2a
```

This downloads the probe binary, registers with the control plane, and installs a systemd service.

### Option B: Manual Linux install (development/testing)

```bash
./bin/probe init --server http://localhost:8080 --token <token>
./bin/probe service install
# or foreground mode:
./bin/probe run
```

### Option C: Manual Windows install (PowerShell, Admin)

```powershell
# Download probe.exe from release assets
.\probe.exe init --server http://<control-plane>:8080 --token <token>
.\probe.exe service install
# lifecycle commands:
.\probe.exe service status
.\probe.exe service stop
.\probe.exe service start
.\probe.exe service remove
```

Windows defaults:
- config: `%ProgramData%\Legator\probe-config\config.yaml`
- data/log: `%ProgramData%\Legator\`

## Step 5: Verify

Open the web UI at `http://localhost:8080/` — your probe should appear in the fleet view.

Or via API:

```bash
# List probes
curl -sf http://localhost:8080/api/v1/probes | jq

# Fleet summary
curl -sf http://localhost:8080/api/v1/fleet/summary | jq

# Send a command
curl -sf -X POST http://localhost:8080/api/v1/probes/<probe-id>/command \
  -H 'Content-Type: application/json' \
  -d '{"command":"hostname","level":"observe"}' \
  | jq
```

## What Happens Next

Once a probe is connected:

1. **Inventory scan** runs automatically — OS, packages, services, users, network interfaces
2. **Heartbeat** every 30 seconds — CPU, memory, disk metrics
3. **Health scoring** updates based on heartbeat data
4. **Commands** can be dispatched via API, UI, or chat

## Policy Levels

Each probe operates at a **capability level** that limits what commands it can execute:

| Level | Can do | Examples |
|---|---|---|
| **observe** | Read-only operations | `ls`, `cat`, `ps`, `df`, `uptime` |
| **diagnose** | observe + analysis/debug | `strace`, `tcpdump`, `dig`, `ping` |
| **remediate** | diagnose + writes/changes | `rm`, `apt install`, `systemctl restart` |

Default: **observe** (read-only). Change via policy templates:

```bash
# List available policies
curl -sf http://localhost:8080/api/v1/policies | jq

# Apply 'diagnose' policy to a probe
curl -sf -X POST http://localhost:8080/api/v1/probes/<id>/apply-policy/diagnose | jq
```

Policy is enforced at **both** the control plane and the probe (defence in depth).

## Approval Queue

Commands that exceed the probe's policy level or are classified as high-risk require human approval:

```bash
# View pending approvals
curl -sf http://localhost:8080/api/v1/approvals?status=pending | jq

# Approve
curl -sf -X POST http://localhost:8080/api/v1/approvals/<id>/decide \
  -H 'Content-Type: application/json' \
  -d '{"decision":"approved","decided_by":"admin"}'
```

Or use the web UI at `/approvals`.

## Configure SSO (Optional)

If you use Keycloak, Auth0, or any OIDC provider, add this to your `legator.json`:

```json
{
  "oidc": {
    "enabled": true,
    "provider_url": "https://keycloak.example.com/realms/my-realm",
    "client_id": "legator",
    "client_secret": "your-client-secret",
    "redirect_url": "https://legator.example.com/auth/oidc/callback",
    "role_mapping": {
      "admins": "admin",
      "developers": "operator"
    }
  }
}
```

Or via environment variables:

```bash
export LEGATOR_OIDC_ENABLED=true
export LEGATOR_OIDC_PROVIDER_URL="https://keycloak.example.com/realms/my-realm"
export LEGATOR_OIDC_CLIENT_ID="legator"
export LEGATOR_OIDC_CLIENT_SECRET="your-client-secret"
export LEGATOR_OIDC_REDIRECT_URL="https://legator.example.com/auth/oidc/callback"
```

The login page will show a "Sign in with SSO" button alongside the local login form.

## Chat (requires LLM configuration)

Talk to your probes in natural language:

```bash
curl -sf -X POST http://localhost:8080/api/v1/probes/<id>/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"What services are running on this server?"}'
```

Or use the web UI at `/probe/<id>/chat`.

## CLI: legatorctl

```bash
# List all probes
./bin/legatorctl list

# Show probe details
./bin/legatorctl info <probe-id>

# JSON output
./bin/legatorctl list --format json
```

## Next Steps

- **Tags**: Organise probes with `PUT /api/v1/probes/<id>/tags`
- **Group commands**: Dispatch to all probes matching a tag with `POST /api/v1/fleet/by-tag/<tag>/command`
- **Webhooks**: Get notified on fleet events via `POST /api/v1/webhooks`
- **Metrics**: Prometheus-compatible metrics at `GET /api/v1/metrics`
- **Audit log**: Full action history at `GET /api/v1/audit` or `/audit` in the web UI

---

*Built with Go. Runs anywhere. No dependencies.*
