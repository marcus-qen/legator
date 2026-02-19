# Data Protection

Data is sacred. InfraAgent enforces this principle at the deepest level of the runtime — with rules that cannot be configured away.

## The Rule

**Data mutations are NEVER automated. No autonomy level unlocks them. No configuration overrides them.**

This is hardcoded in the guardrail engine. It is not a policy. It is not configurable. It is enforced by the binary.

## What's Protected

### Hardcoded Rules (cannot be changed)

These operations are blocked unconditionally, regardless of agent autonomy, Action Sheets, or any other configuration:

| Operation | Why |
|-----------|-----|
| Delete PersistentVolumeClaim | Direct data loss |
| Delete PersistentVolume | Direct data loss |
| Modify PV reclaim policy | Could cause cascading data loss |
| Delete Namespace | Cascading deletion of everything inside |
| Delete database CRs (e.g. cnpg.io/Cluster) | Database destruction |
| Delete S3/MinIO objects | Object storage data loss |

### Declared Data Resources

The AgentEnvironment's `dataResources` section declares what data exists in this environment:

```yaml
dataResources:
  backupMaxAge: "24h"
  databases:
    - kind: cnpg.io/Cluster
      namespace: backstage
      name: backstage-db
      backupSchedule: "0 */6 * * *"
  persistentStorage:
    - kind: PersistentVolumeClaim
      namespace: harbor
      name: harbor-registry
  objectStorage:
    - kind: s3-bucket
      name: cnpg-backups
      provider: minio
```

The runtime builds an O(1) index of all declared data resources. Before any mutation, it checks:

1. **Direct target** — Is the mutation target a declared data resource?
2. **Namespace cascade** — Does the target namespace contain data resources?
3. **Owner chain** — Is the target resource owned by a data resource?

## Pre-Flight Data Impact Check

When an agent attempts a mutation near data resources, the engine runs additional checks:

- **Backup freshness**: When was the last backup? If older than `backupMaxAge`, a warning is logged.
- **Backup destination**: Is the backup destination reachable?

These don't block execution (unless the mutation targets data directly), but they appear in the AgentRun audit trail as warnings.

## Four-Tier Classification

Every tool call is classified into one of four tiers:

```
read → service-mutation → destructive-mutation → data-mutation
```

| Tier | Autonomy Required | Example |
|------|-------------------|---------|
| `read` | `observe` | kubectl get, HTTP GET, logs |
| `service-mutation` | `automate-safe` | restart deployment, delete pod |
| `destructive-mutation` | `automate-destructive` | delete deployment, remove ingress |
| `data-mutation` | **NEVER** | delete PVC, drop database |

An agent with `autonomy: automate-destructive` can do everything **except** data mutations. There is no `automate-data` level. It doesn't exist by design.

## Audit Trail

Every data protection decision is recorded in the AgentRun:

```yaml
status:
  actions:
    - seq: 3
      tool: kubectl.delete
      target: pvc/harbor-registry -n harbor
      tier: data-mutation
      preFlightCheck:
        dataProtection: "BLOCKED"
        reason: "Hardcoded rule: PersistentVolumeClaim deletion is never automated"
      status: blocked
  guardrails:
    actionsBlocked: 1
    checksPerformed: 5
```

## Multi-Cluster Data Protection

Each AgentEnvironment declares its own data resources. When an agent manages a remote cluster, data protection is enforced per-environment — the agent protecting Cluster A knows about Cluster A's databases, not Cluster B's.

## Why This Design?

Autonomous agents are powerful. An LLM with kubectl access can do enormous damage in seconds. The data protection model exists because:

1. **Data loss is irreversible.** You can redeploy a service. You cannot un-delete a database.
2. **LLMs make mistakes.** Even the best models occasionally misinterpret instructions.
3. **Trust is asymmetric.** The cost of a false negative (allowing a data deletion) vastly exceeds the cost of a false positive (blocking a safe operation).
4. **Human oversight for data.** Data operations require human judgment — backup verification, impact assessment, stakeholder notification.

The hardcoded rules are a feature, not a limitation. They mean you can give an agent `automate-destructive` autonomy and sleep soundly knowing your databases are safe.
