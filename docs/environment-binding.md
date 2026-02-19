# Environment Binding

The AgentEnvironment CRD is what makes agents portable. The same InfraAgent + Skills can manage completely different clusters by binding to different environments.

## Concept

```
┌─────────────────┐     ┌──────────────────┐     ┌──────────────────┐
│   InfraAgent    │     │      Skill       │     │ AgentEnvironment │
│  (what/when)    │──→  │  (expertise)     │  +  │  (where/how)     │
│                 │     │                  │     │                  │
│ watchman-light  │     │ endpoint-monitor │     │ dev-lab          │
│ cron: */5       │     │ actions.yaml     │     │ prod-cluster     │
│ tier: fast      │     │ SKILL.md         │     │ staging          │
└─────────────────┘     └──────────────────┘     └──────────────────┘
```

## In-Cluster vs Remote

### In-Cluster (default)

The agent monitors the same cluster the controller runs on:

```yaml
spec:
  connection:
    kind: in-cluster
```

### Remote Cluster

The agent monitors a different cluster via kubeconfig:

```yaml
spec:
  connection:
    kind: kubeconfig
    kubeconfig:
      secretRef: prod-cluster-kubeconfig
      key: kubeconfig  # default
```

Create the Secret:

```bash
kubectl create secret generic prod-cluster-kubeconfig \
  -n agents \
  --from-file=kubeconfig=/path/to/prod-kubeconfig.yaml
```

The controller caches remote clients per Secret version — updating the Secret automatically creates a fresh client.

## Endpoints

Named service endpoints the agent can health-check or query:

```yaml
spec:
  endpoints:
    grafana:
      url: https://grafana.example.com
      healthPath: /api/health
    alertmanager:
      url: http://alertmanager.monitoring:9093
      healthPath: /-/healthy
      internal: true  # cluster-internal, no TLS
```

Skills reference endpoints by name (`endpoints.grafana`), never by URL.

## Namespaces

Grouped by role so skills can reason about the cluster:

```yaml
spec:
  namespaces:
    monitoring:
      - monitoring
      - loki
    apps:
      - backstage
      - my-app
    system:
      - kube-system
      - cert-manager
    additional:
      databases:
        - cnpg-system
```

## Credentials

Named references to Secrets — the agent gets the name, the runtime resolves the value:

```yaml
spec:
  credentials:
    github:
      secretRef: github-pat
      type: token
    harbor:
      secretRef: harbor-admin
      type: basic-auth
```

> **Credential hygiene:** The runtime sanitizes all tool output before recording it in AgentRun audit trails. Secrets are never stored in CR status fields.

## Channels

Notification destinations referenced by name:

```yaml
spec:
  channels:
    oncall:
      type: telegram
      target: "123456789"
      secretRef: telegram-bot-token
    alerts:
      type: slack
      target: https://hooks.slack.com/services/XXX
    ci:
      type: webhook
      target: https://ci.example.com/api/trigger
```

## MCP Servers

External tool servers the agent can call via MCP protocol:

```yaml
spec:
  mcpServers:
    k8sgpt:
      endpoint: http://k8sgpt.agents:8089
      capabilities:
        - k8sgpt.analyze
```

If an MCP server is unavailable, agents degrade gracefully — they continue with reduced capabilities and log a warning.

## Data Resources

See [Data Protection](data-protection.md) for full details.

```yaml
spec:
  dataResources:
    backupMaxAge: "24h"
    databases:
      - kind: cnpg.io/Cluster
        namespace: backstage
        name: backstage-db
    persistentStorage:
      - kind: PersistentVolumeClaim
        namespace: harbor
        name: harbor-registry
    objectStorage:
      - kind: s3-bucket
        name: cnpg-backups
        provider: minio
```
