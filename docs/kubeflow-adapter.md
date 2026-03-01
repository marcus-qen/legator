# Kubeflow Adapter

Legator exposes a Kubeflow integration boundary with **read APIs plus guarded mutations**.

## Scope

- Read-only cluster status endpoint
- Read-only Kubeflow inventory endpoint
- Run/job status endpoint
- Guarded mutation endpoints for submit + cancel
- Optional guarded refresh action endpoint
- kubectl-based adapter/client boundary in control plane
- MCP parity tools for submit/cancel/status when wired by the control-plane surface

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
| `LEGATOR_KUBEFLOW_ACTIONS_ENABLED` | `false` | Enable guarded mutation endpoints |

## API endpoints

### Read-only

- `GET /api/v1/kubeflow/status`
- `GET /api/v1/kubeflow/inventory`
- `GET /api/v1/kubeflow/runs/{name}/status`

These endpoints require `fleet:read`.

### Guarded mutations (disabled by default)

- `POST /api/v1/kubeflow/actions/refresh`
- `POST /api/v1/kubeflow/runs/submit`
- `POST /api/v1/kubeflow/runs/{name}/cancel`

Mutation endpoints require `fleet:write` **and** `LEGATOR_KUBEFLOW_ACTIONS_ENABLED=true`.

When enabled, submit/cancel flow through the same policy/approval pipeline as command dispatch:

- `allow` → action executes immediately
- `queue` → response includes `approval_id`
- `deny` → request rejected with policy rationale payload

If disabled, mutation endpoints return:

```json
{"code":"action_disabled","error":"kubeflow actions are disabled by policy"}
```

## Status transitions

Submit/cancel responses include additive transition payloads:

- `transition.action` (`submit` / `cancel`)
- `transition.before`
- `transition.after`
- `transition.changed`

## Example

```bash
LEGATOR_KUBEFLOW_ENABLED=true \
LEGATOR_KUBEFLOW_ACTIONS_ENABLED=true \
LEGATOR_KUBEFLOW_NAMESPACE=kubeflow \
LEGATOR_KUBEFLOW_CONTEXT=lab \
./bin/control-plane

curl -sf http://localhost:8080/api/v1/kubeflow/status | jq
curl -sf http://localhost:8080/api/v1/kubeflow/inventory | jq
curl -sf http://localhost:8080/api/v1/kubeflow/runs/example/status | jq
```
