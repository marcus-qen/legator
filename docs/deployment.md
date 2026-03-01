# Deployment Guide

This guide covers deploying Legator in production: bare-metal control plane, probe deployment via DaemonSet or systemd, OIDC, LLM, and TLS configuration.

---

## Prerequisites

| Component | Requirement |
|---|---|
| Control plane | Linux amd64/arm64, 512 MB RAM, 1 CPU, 1 GB disk |
| Probe | Linux amd64/arm64 or Windows amd64, 64 MB RAM, no external deps |
| SQLite | Included in binary — no separate database needed |
| TLS | Handled by a reverse proxy (Caddy/Nginx/Traefik) |

---

## 1. Bare Metal — Control Plane

### Build from source

```bash
git clone https://github.com/marcus-qen/legator.git
cd legator
make build
# outputs: bin/control-plane, bin/probe, bin/legatorctl
```

### Release binary (recommended for production)

```bash
# Download from GitHub Releases
curl -Lo control-plane https://github.com/marcus-qen/legator/releases/latest/download/control-plane-linux-amd64
chmod +x control-plane
sudo mv control-plane /usr/local/bin/legator-cp
```

### Minimal startup (dev/test)

```bash
# In-memory state, auto-generated signing key, no auth
./bin/control-plane
# Listens on :8080
```

### Production startup

```bash
LEGATOR_DATA_DIR=/var/lib/legator \
LEGATOR_SIGNING_KEY=$(openssl rand -hex 32) \
LEGATOR_AUTH=true \
LEGATOR_ADDR=:8080 \
./bin/control-plane
```

### systemd service

```ini
# /etc/systemd/system/legator-cp.service
[Unit]
Description=Legator Control Plane
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=legator
Group=legator
EnvironmentFile=/etc/legator/env
ExecStart=/usr/local/bin/legator-cp
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/var/lib/legator /var/log/legator
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

```bash
# /etc/legator/env
LEGATOR_ADDR=:8080
LEGATOR_DATA_DIR=/var/lib/legator
LEGATOR_SIGNING_KEY=<output of: openssl rand -hex 32>
LEGATOR_AUTH=true
# Optional: OIDC, LLM, etc. — see sections below
```

```bash
# Setup
sudo useradd -r -s /sbin/nologin legator
sudo mkdir -p /var/lib/legator /etc/legator
sudo chown legator:legator /var/lib/legator
sudo chmod 700 /var/lib/legator
sudo chmod 600 /etc/legator/env

sudo systemctl daemon-reload
sudo systemctl enable --now legator-cp
sudo systemctl status legator-cp
```

### Configuration reference

All config can be set via environment variables:

| Variable | Default | Description |
|---|---|---|
| `LEGATOR_ADDR` | `:8080` | Listen address |
| `LEGATOR_DATA_DIR` | (in-memory) | Persistent data directory (SQLite, releases) |
| `LEGATOR_SIGNING_KEY` | auto-generated | 32-byte hex signing key — regenerating invalidates existing probes |
| `LEGATOR_AUTH` | `false` | Enable multi-user auth |
| `LEGATOR_LLM_PROVIDER` | — | LLM provider: `openai`, `anthropic`, `ollama` |
| `LEGATOR_LLM_API_KEY` | — | LLM API key |
| `LEGATOR_LLM_MODEL` | — | LLM model name (e.g. `gpt-4o`, `claude-3-5-sonnet-20241022`) |
| `LEGATOR_LLM_BASE_URL` | — | Override LLM API base URL (Ollama, Azure OpenAI, etc.) |
| `LEGATOR_OIDC_ENABLED` | `false` | Enable OIDC SSO |
| `LEGATOR_OIDC_ISSUER` | — | OIDC provider issuer URL |
| `LEGATOR_OIDC_CLIENT_ID` | — | OIDC client ID |
| `LEGATOR_OIDC_CLIENT_SECRET` | — | OIDC client secret |
| `LEGATOR_OIDC_REDIRECT_URL` | — | Full callback URL (must match provider config) |
| `LEGATOR_SERVER_URL` | — | Public URL of control plane (used in install_command) |
| `LEGATOR_GRAFANA_ENABLED` | `false` | Enable Grafana adapter |
| `LEGATOR_GRAFANA_BASE_URL` | — | Grafana base URL |
| `LEGATOR_GRAFANA_API_TOKEN` | — | Grafana API token |
| `LEGATOR_KUBEFLOW_ENABLED` | `false` | Enable Kubeflow adapter |
| `LEGATOR_KUBEFLOW_KUBECONFIG` | — | Path to kubeconfig |
| `LEGATOR_KUBEFLOW_ACTIONS_ENABLED` | `false` | Enable Kubeflow mutation endpoints |
| `LEGATOR_JOBS_RETRY_MAX_ATTEMPTS` | `1` | Default job retry max attempts |
| `LEGATOR_JOBS_RETRY_INITIAL_BACKOFF` | `5s` | Default initial retry delay |
| `LEGATOR_JOBS_RETRY_MULTIPLIER` | `2` | Default retry backoff multiplier |

---

## 2. Probe — Bare Metal (curl|bash)

The easiest path: generate a token, run the one-liner.

### Step 1: Generate a registration token

```bash
# On the control plane (or via the UI)
curl -sf -X POST -H "Authorization: Bearer lgk_..." \
     https://legator.example.com/api/v1/tokens | jq .
```

Response includes `install_command` — a fully formed curl|bash command.

### Step 2: Run on each target host

```bash
curl -sSL https://legator.example.com/install.sh | \
  sudo bash -s -- \
    --server https://legator.example.com \
    --token <token>
```

The install script:
1. Detects architecture (amd64/arm64)
2. Downloads the probe binary from `<server>/download/probe-<version>-<os>-<arch>`
3. Creates `legator` user, `/etc/legator`, `/var/lib/legator`, `/var/log/legator`
4. Writes `/etc/legator/probe.yaml` with server URL and API key
5. Installs and starts `legator-probe.service` (systemd)
6. Calls `POST /api/v1/register` to get probe ID + API key

### Install script options

```bash
install.sh --server <url> --token <token> [options]
  --arch <arch>           Override arch detection (amd64|arm64)
  --version <version>     Pin binary version (default: latest)
  --config-dir <path>     Config directory (default: /etc/legator)
  --no-start              Install only, do not start service
  --github-release        Download from GitHub Releases
```

### Probe config file

```yaml
# /etc/legator/probe.yaml
server: wss://legator.example.com
probe_id: prb-a1b2c3d4
api_key: lgk_<64hex>
policy_level: observe
tags:
  - web
  - prod
  - region-eu
```

### Probe systemd service

The install script creates `/etc/systemd/system/legator-probe.service`. To manage:

```bash
sudo systemctl status legator-probe
sudo journalctl -u legator-probe -f
sudo systemctl restart legator-probe
```

---

## 3. Probe — Kubernetes DaemonSet

Deploy probes to every node in a cluster. Each probe registers independently.

### Step 1: Generate a multi-use token

```bash
curl -sf -X POST -H "Authorization: Bearer lgk_..." \
  "https://legator.example.com/api/v1/tokens?multi_use=true&no_expiry=true" | jq .token
```

### Step 2: Create Secret

```bash
kubectl create secret generic legator-probe-config \
  --namespace legator-system \
  --from-literal=server=wss://legator.example.com \
  --from-literal=token=<multi-use-token>
```

### Step 3: Apply DaemonSet

```yaml
# legator-probe-daemonset.yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: legator-probe
  namespace: legator-system
spec:
  selector:
    matchLabels:
      app: legator-probe
  template:
    metadata:
      labels:
        app: legator-probe
    spec:
      hostPID: true
      hostNetwork: true
      tolerations:
        - operator: Exists  # run on all nodes including control-plane
      initContainers:
        - name: register
          image: curlimages/curl:latest
          command:
            - sh
            - -c
            - |
              curl -sSL "$SERVER/install.sh" | \
                bash -s -- \
                  --server "$SERVER" \
                  --token "$TOKEN" \
                  --no-start \
                  --config-dir /etc/legator-probe
          env:
            - name: SERVER
              valueFrom:
                secretKeyRef:
                  name: legator-probe-config
                  key: server
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: legator-probe-config
                  key: token
          volumeMounts:
            - name: config
              mountPath: /etc/legator-probe
      containers:
        - name: probe
          image: ghcr.io/marcus-qen/legator/probe:latest
          args:
            - --config=/etc/legator-probe/probe.yaml
          env:
            - name: LEGATOR_HOSTNAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          securityContext:
            privileged: true  # required for host metrics
          volumeMounts:
            - name: config
              mountPath: /etc/legator-probe
            - name: proc
              mountPath: /host/proc
              readOnly: true
            - name: sys
              mountPath: /host/sys
              readOnly: true
      volumes:
        - name: config
          emptyDir: {}
        - name: proc
          hostPath:
            path: /proc
        - name: sys
          hostPath:
            path: /sys
```

```bash
kubectl apply -f legator-probe-daemonset.yaml
kubectl -n legator-system rollout status daemonset/legator-probe
```

---

## 4. OIDC Setup (Keycloak Example)

### Keycloak client config

1. Create a new client: `legator`
2. Client settings:
   - **Access Type:** confidential
   - **Standard Flow:** enabled
   - **Implicit Flow:** disabled (important)
   - **Valid Redirect URIs:** `https://legator.example.com/auth/oidc/callback`
   - **PKCE:** `S256` (Keycloak 18+: Proof Key for Code Exchange enabled)
3. Note the **client secret** from the Credentials tab.

### Legator env vars

```bash
LEGATOR_OIDC_ENABLED=true
LEGATOR_OIDC_ISSUER=https://keycloak.example.com/realms/your-realm
LEGATOR_OIDC_CLIENT_ID=legator
LEGATOR_OIDC_CLIENT_SECRET=<from-keycloak-credentials-tab>
LEGATOR_OIDC_REDIRECT_URL=https://legator.example.com/auth/oidc/callback
```

### Role mapping

Add a mapper in Keycloak to include roles in the ID token:
- **Mapper type:** User Attribute or Realm Role
- **Token Claim Name:** `legator_role`
- **Claim value:** `admin`, `operator`, or `viewer`

Legator reads `legator_role` from the ID token claims to assign the user's role on first login.

### Other OIDC providers

| Provider | Issuer format |
|---|---|
| Auth0 | `https://<your-tenant>.auth0.com/` |
| Okta | `https://<your-okta-domain>/oauth2/default` |
| Google | `https://accounts.google.com` |
| Authentik | `https://auth.example.com/application/o/<slug>/` |
| Dex | `https://dex.example.com` |

---

## 5. LLM Provider Setup

Legator's task runner, probe chat, and fleet chat require an LLM provider. Three options:

### Option A: Environment variables (simple)

```bash
# OpenAI
LEGATOR_LLM_PROVIDER=openai
LEGATOR_LLM_MODEL=gpt-4o
LEGATOR_LLM_API_KEY=sk-...

# Anthropic
LEGATOR_LLM_PROVIDER=anthropic
LEGATOR_LLM_MODEL=claude-3-5-sonnet-20241022
LEGATOR_LLM_API_KEY=sk-ant-...

# Ollama (local)
LEGATOR_LLM_PROVIDER=openai       # Ollama is OpenAI-compatible
LEGATOR_LLM_BASE_URL=http://ollama:11434/v1
LEGATOR_LLM_MODEL=llama3.1:70b
LEGATOR_LLM_API_KEY=ollama        # any non-empty value
```

### Option B: Model Dock (runtime-configurable, BYOK)

1. Navigate to `/model-dock` in the web UI (or `GET /api/v1/model-profiles`)
2. Create a profile: `POST /api/v1/model-profiles`
3. Activate it: `POST /api/v1/model-profiles/{id}/activate`

Model Dock profiles take precedence over env vars when active. Supports multiple profiles for routing different tasks to different models.

### Option C: No LLM

Legator works without an LLM. Fleet management, probes, policies, alerts, jobs, and audit all function. Chat, task runner, and fleet chat return `503 Service Unavailable`.

---

## 6. TLS Termination

Legator itself speaks plain HTTP. Put a TLS-terminating reverse proxy in front.

### Caddy (recommended)

```caddyfile
# /etc/caddy/Caddyfile
legator.example.com {
    reverse_proxy localhost:8080
    tls /etc/legator/tls/cert.pem /etc/legator/tls/key.pem
    # or use automatic ACME: just the domain is enough
}
```

### Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name legator.example.com;

    ssl_certificate /etc/letsencrypt/live/legator.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/legator.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-Host $host;
        proxy_read_timeout 3600;  # SSE and WebSocket need long timeouts
    }
}
```

> The `X-Forwarded-Proto` and `X-Forwarded-Host` headers are used by Legator to generate correct `install_command` URLs in token responses.

### Traefik (Kubernetes Ingress)

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: legator-cp
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: websecure
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  tls:
    - hosts: [legator.example.com]
      secretName: legator-tls
  rules:
    - host: legator.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: legator-cp
                port:
                  number: 8080
```

---

## 7. Probe Self-Update

Probes support in-place binary update via `POST /api/v1/probes/{id}/update`:

1. Upload new binary to `$LEGATOR_DATA_DIR/releases/probe-<version>-linux-amd64`
2. Call the update endpoint:
   ```bash
   curl -sf -X POST \
     -H "Authorization: Bearer lgk_..." \
     -H "Content-Type: application/json" \
     -d '{"url": "https://legator.example.com/download/probe-1.0.1-linux-amd64", "version": "1.0.1"}' \
     https://legator.example.com/api/v1/probes/prb-a1b2c3d4/update
   ```
3. The control plane sends the `UpdatePayload` over the WebSocket
4. The probe downloads the binary, verifies SHA256 (if provided), replaces itself, and restarts via systemd

**Fleet-wide update:** Use `POST /api/v1/fleet/by-tag/{tag}/command` with a shell command, or automate via Jobs (`POST /api/v1/jobs` with a cron that calls the update API).

---

## 8. Upgrading the Control Plane

```bash
# Download new binary
curl -Lo /tmp/control-plane-new \
  https://github.com/marcus-qen/legator/releases/latest/download/control-plane-linux-amd64
chmod +x /tmp/control-plane-new

# Verify
/tmp/control-plane-new --version

# Replace and restart
sudo mv /tmp/control-plane-new /usr/local/bin/legator-cp
sudo systemctl restart legator-cp
sudo systemctl status legator-cp
```

SQLite databases are forward-compatible across minor versions. Always check CHANGELOG.md for migration notes before major version upgrades.

---

## 9. Persistent Data Layout

When `LEGATOR_DATA_DIR=/var/lib/legator`:

```
/var/lib/legator/
├── fleet.db          # Fleet state (probes, API keys, tags)
├── audit.db          # Audit log (immutable event log)
├── chat.db           # Chat message history
├── users.db          # User accounts and sessions
├── alerts.db         # Alert rules and routing policies
├── jobs.db           # Scheduled jobs and run history
├── releases/         # Uploaded probe binaries for self-update
│   └── probe-1.0.1-linux-amd64
└── ...
```

Backup: `sqlite3 /var/lib/legator/fleet.db ".backup /backup/fleet-$(date +%Y%m%d).db"`

---

## 10. Health Check

```bash
curl -sf https://legator.example.com/healthz
# → ok

curl -sf https://legator.example.com/version
# → {"version":"1.0.0-beta.1","commit":"abc123","date":"2026-03-01"}
```
