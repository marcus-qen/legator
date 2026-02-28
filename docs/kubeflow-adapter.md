# Kubeflow Adapter (MVP)

Legator now exposes a first Kubeflow integration boundary focused on **safe read-only data**.

## Scope (this MVP)

- Read-only cluster status endpoint
- Read-only Kubeflow inventory endpoint
- Optional guarded refresh action endpoint (disabled by default)
- kubectl-based adapter/client boundary in control plane

## Configuration

Set these environment variables on the control plane:

| Variable | Default | Purpose |
|---|---|---|
| `LEGATOR_KUBEFLOW_ENABLED` | `false` | Enable Kubeflow endpoints |
| `LEGATOR_KUBEFLOW_NAMESPACE` | `kubeflow` | Namespace scanned for Kubeflow resources |
| `LEGATOR_KUBEFLOW_KUBECONFIG` | — | Optional kubeconfig path for kubectl |
| `LEGATOR_KUBEFLOW_CONTEXT` | — | Optional kubeconfig context |
| `LEGATOR_KUBEFLOW_CLI_PATH` | `kubectl` | kubectl binary path/name |
| `LEGATOR_KUBEFLOW_TIMEOUT` | `15s` | Timeout per kubectl call |
| `LEGATOR_KUBEFLOW_ACTIONS_ENABLED` | `false` | Enable guarded refresh action |

## API endpoints

### Read-only

- `GET /api/v1/kubeflow/status`
- `GET /api/v1/kubeflow/inventory`

These endpoints require `fleet:read`.

### Guarded action (disabled by default)

- `POST /api/v1/kubeflow/actions/refresh`

This endpoint requires `fleet:write` **and** `LEGATOR_KUBEFLOW_ACTIONS_ENABLED=true`.

If disabled, it returns:

```json
{"code":"action_disabled","error":"kubeflow actions are disabled by policy"}
```

## Example

```bash
LEGATOR_KUBEFLOW_ENABLED=true \
LEGATOR_KUBEFLOW_NAMESPACE=kubeflow \
LEGATOR_KUBEFLOW_CONTEXT=lab \
./bin/control-plane

curl -sf http://localhost:8080/api/v1/kubeflow/status | jq
curl -sf http://localhost:8080/api/v1/kubeflow/inventory | jq
```
