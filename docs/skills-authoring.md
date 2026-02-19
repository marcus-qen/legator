# Writing Skills

A skill is an agent's expertise — the instructions and declared actions that define what the agent knows how to do. Skills are portable: the same skill works across different clusters by binding to different AgentEnvironments.

## Skill Structure

A skill is a directory containing:

```
my-skill/
├── SKILL.md          # Instructions (required)
└── actions.yaml      # Action Sheet (required)
```

## SKILL.md

The SKILL.md file contains YAML frontmatter followed by Markdown instructions:

```markdown
---
name: endpoint-monitoring
description: Monitor service endpoints and alert on failures
version: 1.0.0
author: platform-team
tags:
  - monitoring
  - health-checks
---

# Endpoint Monitoring

You are responsible for monitoring the health of all platform endpoints.

## Instructions

1. Check each endpoint listed in `endpoints` by hitting its `healthPath`
2. For failed endpoints, check if the backing pods are running
3. If pods are crashlooping, check recent logs for error patterns
4. Report findings with severity:
   - CRITICAL: Data-bearing service down, multiple services affected
   - WARNING: Single non-data service degraded
   - INFO: Transient errors, self-healing detected

## Available Data

- `endpoints` — map of named service URLs and health paths
- `namespaces.monitoring` — where Prometheus/Alertmanager live
- `namespaces.apps` — application namespaces to check

## Constraints

- Never restart more than one deployment per run
- Always check pod status before restarting
- Escalate if >3 services are simultaneously unhealthy
```

### Frontmatter Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | ✅ | Unique skill identifier |
| `description` | ✅ | One-line summary |
| `version` | ✅ | Semantic version |
| `author` | | Skill author |
| `tags` | | Searchable tags |

## actions.yaml

The Action Sheet declares every action the agent can take, with risk classification and constraints:

```yaml
actions:
  - id: check-endpoint-health
    description: HTTP GET to check endpoint availability
    tool: http.get
    pattern: "http.get *"
    tier: read
    cooldown: 0s

  - id: get-pods
    description: List pods in a namespace
    tool: kubectl.get
    pattern: "kubectl.get pods*"
    tier: read
    cooldown: 0s

  - id: describe-pod
    description: Get detailed pod information
    tool: kubectl.describe
    pattern: "kubectl.describe pod/*"
    tier: read
    cooldown: 0s

  - id: restart-deployment
    description: Rolling restart a deployment
    tool: kubectl.rollout
    pattern: "kubectl.rollout restart deployment/*"
    tier: service-mutation
    cooldown: 300s
    preConditions:
      - type: check
        description: Verify pods are not already restarting
        tool: kubectl.get
        args: "pods -l app=${deployment} --field-selector=status.phase!=Running"
        expect: "No resources found"
    dataImpact:
      description: May briefly affect service availability
      severity: low
    rollback:
      description: "Deployment will roll back automatically on failure"
      automatic: true

  - id: delete-pod
    description: Delete a single pod to force reschedule
    tool: kubectl.delete
    pattern: "kubectl.delete pod/*"
    tier: service-mutation
    cooldown: 60s
```

### Action Fields

| Field | Required | Description |
|-------|----------|-------------|
| `id` | ✅ | Unique action identifier |
| `description` | ✅ | What this action does |
| `tool` | ✅ | Tool identifier |
| `pattern` | ✅ | Glob pattern for matching tool calls |
| `tier` | ✅ | `read`, `service-mutation`, `destructive-mutation`, `data-mutation` |
| `cooldown` | | Minimum time between executions (e.g. `300s`) |
| `preConditions` | | Checks that must pass before execution |
| `dataImpact` | | Description of data implications |
| `rollback` | | How to undo this action |

### Tier Classification

| Tier | Risk | Examples |
|------|------|---------|
| `read` | None | GET requests, kubectl get/describe/logs |
| `service-mutation` | Service disruption | Restart deployment, delete pod, scale |
| `destructive-mutation` | Irreversible | Delete deployment, delete service |
| `data-mutation` | Data loss | Delete PVC, drop database, delete namespace |

> ⚠️ **Data mutations are ALWAYS blocked by the runtime, regardless of autonomy level.** There is no configuration that allows automated data deletion.

## Skill Sources

Skills can be loaded from three sources:

### Bundled

Shipped with agent examples:

```yaml
skills:
  - name: endpoint-monitoring
    source: bundled
```

### ConfigMap

Stored as a ConfigMap in the agent's namespace:

```yaml
skills:
  - name: my-skill
    source: configmap
```

The ConfigMap must contain `SKILL.md` and `actions.yaml` as data keys.

### Git

Loaded from a Git repository:

```yaml
skills:
  - name: monitoring
    source: "git://github.com/myorg/agent-skills#skills/monitoring@v1.0.0"
```

Format: `git://host/org/repo#path@ref`

- `ref` can be a tag, branch, or commit SHA
- Only the skill directory is cloned (shallow + sparse)
- Skills are cached and re-pulled on version change

## Validation

The runtime validates skills at load time:

1. **SKILL.md** must exist with valid frontmatter (name, description, version)
2. **actions.yaml** must exist with at least one action
3. Each action must have: id, description, tool, pattern, tier
4. Action IDs must be unique within the skill
5. Tier values must be valid enum values

Invalid skills are rejected and the error appears in the InfraAgent's status conditions.

## Best Practices

1. **Declare every action.** Undeclared actions are denied. The Action Sheet is an allowlist.
2. **Set appropriate cooldowns.** Mutations should have cooldowns to prevent rapid-fire changes.
3. **Write pre-conditions.** A restart should check that pods aren't already restarting.
4. **Be specific with patterns.** `kubectl.get pods*` is better than `kubectl.get *`.
5. **Document rollback.** Every mutation should explain how to undo it.
6. **Keep skills focused.** One skill per domain (monitoring, deployment, triage).
7. **Don't hardcode cluster details.** Skills reference environment data (`endpoints`, `namespaces`), not specific URLs or IPs.
