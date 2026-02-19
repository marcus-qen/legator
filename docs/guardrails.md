# Guardrails Deep-Dive

The guardrail system is the safety-critical core of InfraAgent. It physically blocks actions that exceed an agent's authority — the LLM never sees the result of a blocked action.

## Defence in Depth

Every tool call passes through five independent checks:

```
Tool Call → Action Match → Tier Classify → Autonomy Check → Allow/Deny Check → Cooldown Check → Execute or Block
                                    ↓                ↓                ↓              ↓
                              Data Protection   Budget Limit    Rate Limit    Pre-Conditions
```

A failure at any stage blocks execution. The checks are **runtime-enforced**, not prompt-advisory — the engine sits between the LLM and the tool executor.

## Graduated Autonomy

Four levels, each unlocking additional action tiers:

| Level | Can Execute | Use Case |
|-------|-------------|----------|
| `observe` | Read only | Monitoring, data collection, health checks |
| `recommend` | Read only + generates recommendations | Triage, analysis, advisory |
| `automate-safe` | Read + service-mutation | Restart deployments, delete pods, scale |
| `automate-destructive` | Read + service + destructive-mutation | Delete deployments, remove services |

> **There is no `automate-data` level.** Data mutations (PVC/PV/namespace/database deletion) are always blocked. See [Data Protection](data-protection.md).

## Action Sheets

Every skill declares its actions in `actions.yaml`. This is an **allowlist** — undeclared actions are denied.

```yaml
actions:
  - id: restart-deployment
    tool: kubectl.rollout
    pattern: "kubectl.rollout restart deployment/*"
    tier: service-mutation
    cooldown: 300s
```

The runtime matches each LLM tool call against declared actions:
- **Match found**: proceed to tier/autonomy checks
- **No match**: denied immediately ("undeclared action")

### Pattern Matching

Patterns use glob syntax:

| Pattern | Matches |
|---------|---------|
| `kubectl.get *` | Any kubectl get |
| `kubectl.get pods*` | kubectl get pods, kubectl get pods -n foo |
| `http.get https://grafana*` | Any Grafana URL |
| `kubectl.rollout restart deployment/*` | Restart any deployment |

## Allow/Deny Lists

In addition to Action Sheets, the InfraAgent spec has allow/deny lists:

```yaml
guardrails:
  allowedActions:
    - "http.get *"
    - "kubectl.get *"
    - "kubectl.rollout restart deployment/*"
  deniedActions:
    - "kubectl.delete deployment/*"
    - "kubectl.delete namespace/*"
```

**Deny always wins.** If an action matches both lists, it's denied.

Evaluation order:
1. Check deny list → if match, block
2. Check allow list → if no match, block
3. Check Action Sheet → if undeclared, block
4. Check autonomy level → if tier exceeds level, block
5. Check cooldown → if too recent, block
6. Execute

## Cooldowns

Per-(agent, action, target) cooldown tracking prevents rapid-fire mutations:

```yaml
- id: restart-deployment
  cooldown: 300s  # 5 minutes between restarts of the same deployment
```

If the agent tries to restart the same deployment twice within 5 minutes, the second attempt is blocked.

## Pre-Conditions

Actions can declare pre-conditions that must pass before execution:

```yaml
preConditions:
  - type: check
    description: Verify pods are not already restarting
    tool: kubectl.get
    args: "pods -l app=${deployment} --field-selector=status.phase!=Running"
    expect: "No resources found"
```

If a pre-condition fails, the action is blocked and the failure reason is recorded in the audit trail.

## Budget Enforcement

Three independent budget limits, each enforced independently:

| Budget | Default | What Happens |
|--------|---------|--------------|
| `tokenBudget` | 50,000 | Run terminated, phase=Failed |
| `maxIterations` | 10 | Run terminated, phase=Failed |
| `timeout` (wall clock) | 120s | Context cancelled, phase=Failed |

## Rate Limiting

The controller enforces cluster-wide and per-agent rate limits:

| Limit | Default | Description |
|-------|---------|-------------|
| Max concurrent (cluster) | 10 | Total simultaneous runs |
| Max concurrent (per-agent) | 1 | No overlapping runs |
| Max runs/hour (cluster) | 200 | Sliding window |
| Max runs/hour (per-agent) | 30 | Sliding window |
| Burst allowance | +3 | Extra for webhook triggers |

## Escalation

When an action exceeds the agent's autonomy level:

1. The action is **blocked** (not executed)
2. An escalation is sent to the configured channel
3. The run waits for the `escalation.timeout` duration
4. On timeout, the `onTimeout` policy executes:
   - `cancel`: Run ends with phase=Escalated
   - `proceed`: Action is executed (dangerous — use carefully)
   - `retry`: Run restarts from the beginning

```yaml
guardrails:
  escalation:
    target: channel
    channelName: oncall
    timeout: "300s"
    onTimeout: cancel
```

## Metrics

The guardrail system emits Prometheus metrics:

- `infraagent_guardrail_blocks_total{agent,action,tier}` — every block counted
- `infraagent_escalations_total{agent,reason}` — every escalation counted
- `infraagent_runs_total{agent,status}` — run outcomes

## Audit Trail

Every guardrail decision is recorded in the AgentRun CR:

```yaml
status:
  guardrails:
    checksPerformed: 12
    actionsBlocked: 2
    escalationsTriggered: 1
    autonomyCeiling: automate-safe
    budgetUsed:
      tokensUsed: 6234
      tokenBudget: 50000
      iterationsUsed: 4
      maxIterations: 10
```

Each individual action record includes the full pre-flight check result, making the audit trail forensically complete.
