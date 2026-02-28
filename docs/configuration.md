# Configuration Reference

Legator is configured through environment variables and/or a JSON config file (`legator.json`).

## Config File

The control plane looks for `legator.json` in:
1. Current working directory
2. `$LEGATOR_CONFIG_FILE` (explicit path)

## Environment Variables

Environment variables override config file values. All vars are prefixed with `LEGATOR_`.

### Core

| Variable | Config Key | Default | Description |
|---|---|---|---|
| `LEGATOR_LISTEN_ADDR` | `listen_addr` | `:8080` | HTTP listen address |
| `LEGATOR_DATA_DIR` | `data_dir` | `/var/lib/legator` | SQLite database directory |
| `LEGATOR_SIGNING_KEY` | `signing_key` | auto-generated | HMAC-SHA256 key for command signing (hex, 64+ chars) |

### Authentication

| Variable | Config Key | Default | Description |
|---|---|---|---|
| `LEGATOR_AUTH` | `auth_enabled` | `false` | Enable authentication (API keys + session login) |

### OIDC (Optional SSO)

| Variable | Config Key | Default | Description |
|---|---|---|---|
| `LEGATOR_OIDC_ENABLED` | `oidc.enabled` | `false` | Enable OIDC authentication |
| `LEGATOR_OIDC_PROVIDER_URL` | `oidc.provider_url` | — | OIDC discovery URL (e.g. `https://keycloak.example.com/realms/my-realm`) |
| `LEGATOR_OIDC_CLIENT_ID` | `oidc.client_id` | — | OIDC client ID |
| `LEGATOR_OIDC_CLIENT_SECRET` | `oidc.client_secret` | — | OIDC client secret (confidential clients) |
| `LEGATOR_OIDC_REDIRECT_URL` | `oidc.redirect_url` | — | OIDC callback URL (`https://your-legator/auth/oidc/callback`) |
| — | `oidc.scopes` | `["openid","email","profile"]` | OIDC scopes to request |
| — | `oidc.role_claim` | `groups` | ID token claim to extract roles from |
| — | `oidc.role_mapping` | `{}` | Map claim values to Legator roles (e.g. `{"platform-admins": "admin"}`) |
| — | `oidc.default_role` | `viewer` | Role for OIDC users not matching any mapping |
| — | `oidc.auto_create_users` | `true` | Auto-create Legator users on first OIDC login |
| — | `oidc.provider_name` | `SSO` | Display name on login page button |

### LLM Integration

| Variable | Config Key | Default | Description |
|---|---|---|---|
| `LEGATOR_LLM_PROVIDER` | — | — | LLM provider name (e.g. `openai`) |
| `LEGATOR_LLM_BASE_URL` | — | — | LLM API base URL |
| `LEGATOR_LLM_API_KEY` | — | — | LLM API key |
| `LEGATOR_LLM_MODEL` | — | — | LLM model name (e.g. `gpt-4o-mini`) |
| `LEGATOR_TASK_APPROVAL_WAIT` | — | `2m` | Time to wait for approval before timing out |

### Additional Settings

| Variable | Config Key | Default | Description |
|---|---|---|---|
| `LEGATOR_TLS_CERT` | `tls_cert` | — | TLS certificate path (enable HTTPS/WSS when paired with key) |
| `LEGATOR_TLS_KEY` | `tls_key` | — | TLS private key path |
| `LEGATOR_LOG_LEVEL` | `log_level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `LEGATOR_RATE_LIMIT` | `rate_limit.requests_per_minute` | `120` | Per-key request limit per minute |
| `LEGATOR_KUBEFLOW_ENABLED` | `kubeflow.enabled` | `false` | Enable Kubeflow adapter routes |
| `LEGATOR_KUBEFLOW_NAMESPACE` | `kubeflow.namespace` | `kubeflow` | Namespace used for Kubeflow resource reads |
| `LEGATOR_KUBEFLOW_KUBECONFIG` | `kubeflow.kubeconfig` | — | Optional kubeconfig path for kubectl |
| `LEGATOR_KUBEFLOW_CONTEXT` | `kubeflow.context` | — | Optional kubeconfig context override |
| `LEGATOR_KUBEFLOW_CLI_PATH` | `kubeflow.cli_path` | `kubectl` | kubectl binary path/name |
| `LEGATOR_KUBEFLOW_TIMEOUT` | `kubeflow.timeout` | `15s` | Timeout per kubectl command |
| `LEGATOR_KUBEFLOW_ACTIONS_ENABLED` | `kubeflow.actions_enabled` | `false` | Enable guarded Kubeflow action endpoint (`POST /api/v1/kubeflow/actions/refresh`) |
| `LEGATOR_GRAFANA_ENABLED` | `grafana.enabled` | `false` | Enable Grafana adapter routes |
| `LEGATOR_GRAFANA_BASE_URL` | `grafana.base_url` | — | Grafana base URL for read-only adapter calls |
| `LEGATOR_GRAFANA_API_TOKEN` | `grafana.api_token` | — | Optional Bearer token for Grafana API |
| `LEGATOR_GRAFANA_TIMEOUT` | `grafana.timeout` | `10s` | Timeout per Grafana API request |
| `LEGATOR_GRAFANA_DASHBOARD_LIMIT` | `grafana.dashboard_limit` | `10` | Maximum dashboards scanned per snapshot (capped at 100) |
| `LEGATOR_GRAFANA_TLS_SKIP_VERIFY` | `grafana.tls_skip_verify` | `false` | Skip TLS verification for self-signed certs |
| `LEGATOR_GRAFANA_ORG_ID` | `grafana.org_id` | `0` | Optional Grafana org ID header (`X-Grafana-Org-Id`) |
| `LEGATOR_EXTERNAL_URL` | `external_url` | — | Public URL used in generated install commands |

### Example `legator.json`

```json
{
  "listen_addr": ":8080",
  "data_dir": "/var/lib/legator",
  "tls_cert": "",
  "tls_key": "",
  "auth_enabled": false,
  "signing_key": "",
  "llm": {
    "provider": "",
    "base_url": "",
    "api_key": "",
    "model": ""
  },
  "rate_limit": {
    "requests_per_minute": 120
  },
  "kubeflow": {
    "enabled": false,
    "namespace": "kubeflow",
    "kubeconfig": "",
    "context": "",
    "cli_path": "kubectl",
    "timeout": "15s",
    "actions_enabled": false
  },
  "grafana": {
    "enabled": false,
    "base_url": "",
    "api_token": "",
    "timeout": "10s",
    "dashboard_limit": 10,
    "tls_skip_verify": false,
    "org_id": 0
  },
  "log_level": "info",
  "external_url": "",
  "oidc": {
    "enabled": false,
    "provider_url": "",
    "client_id": "",
    "client_secret": "",
    "redirect_url": "",
    "scopes": ["openid", "email", "profile"],
    "role_claim": "groups",
    "role_mapping": {},
    "default_role": "viewer",
    "auto_create_users": true,
    "provider_name": "SSO"
  }
}
```

> Tip: leave `signing_key` empty to auto-generate on startup, or set it explicitly in production for stable command signing.
