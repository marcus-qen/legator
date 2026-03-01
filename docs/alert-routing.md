# Alert Routing & Escalation Policy as Code

Legator's alert routing system lets you codify **who owns an alert** and **how it escalates** —
as persistent, versionable policy objects rather than hard-coded webhook targets.

## Concepts

### RoutingPolicy

A `RoutingPolicy` maps alert characteristics to an owner/team and optionally to an
`EscalationPolicy`. Policies are matched in priority order (highest first).

```json
{
  "id": "...",
  "name": "disk-team",
  "owner_label": "team-storage",
  "owner_contact": "storage@example.com",
  "runbook_url": "https://runbooks.example.com/disk-full",
  "escalation_policy_id": "...",
  "priority": 50,
  "is_default": false,
  "matchers": [
    {"field": "condition_type", "op": "eq", "value": "disk_threshold"},
    {"field": "severity",       "op": "eq", "value": "critical"}
  ]
}
```

**Matcher fields:**
| Field | Matches against |
|-------|----------------|
| `condition_type` | Alert rule condition type (`probe_offline`, `disk_threshold`, `cpu_threshold`) |
| `severity` | `AlertCondition.severity` on the rule (`critical`, `warning`, `info`) |
| `rule_name` | Alert rule name |
| `tag` | Any probe tag in the rule condition |

**Matcher ops:** `eq` (default), `contains`, `prefix` — all case-insensitive.

**Matching logic:** All matchers in a policy are AND-ed. A policy with zero matchers
matches every alert (wildcard).

### EscalationPolicy

An `EscalationPolicy` defines an ordered chain of notification steps.

```json
{
  "id": "...",
  "name": "page-on-call",
  "steps": [
    {"order": 1, "target": "on-call-engineer", "target_type": "oncall",   "delay_minutes": 0},
    {"order": 2, "target": "team-lead",         "target_type": "email",   "delay_minutes": 15},
    {"order": 3, "target": "#incidents",         "target_type": "webhook", "delay_minutes": 30,
     "runbook_url": "https://runbooks.example.com/critical"}
  ]
}
```

**Target types:** `oncall`, `email`, `webhook`, `team`.

## Routing Resolution

When an alert fires, the engine resolves routing via `POST /api/v1/alerts/routing/resolve`:

```json
{
  "rule_id": "...",
  "rule_name": "High Disk Usage",
  "condition_type": "disk_threshold",
  "severity": "critical",
  "tags": ["production", "database"],
  "probe_id": "probe-abc"
}
```

Response:

```json
{
  "rule_id": "...",
  "probe_id": "probe-abc",
  "policy_id": "...",
  "policy_name": "disk-team",
  "owner_label": "team-storage",
  "owner_contact": "storage@example.com",
  "runbook_url": "https://runbooks.example.com/disk-full",
  "escalation_policy_id": "...",
  "escalation_steps": [...],
  "explain": {
    "matched_by": "condition_type=disk_threshold, severity=critical",
    "fallback_used": false,
    "reason": "matched by condition_type=disk_threshold, severity=critical"
  }
}
```

### Precedence rules

1. Non-default policies (no `is_default`) whose matchers ALL match — ordered by `priority` DESC; highest wins.
2. If none match, the highest-priority `is_default` policy is used as fallback.
3. If no policies exist, `policy_name: "none"` is returned with `fallback_used: true`.

Specific policies always beat default/fallback policies regardless of priority number.

## API Reference

### Routing Policies

| Method | Endpoint | Permission | Description |
|--------|----------|------------|-------------|
| GET | `/api/v1/alerts/routing/policies` | fleet:read | List all routing policies |
| POST | `/api/v1/alerts/routing/policies` | fleet:write | Create a routing policy |
| GET | `/api/v1/alerts/routing/policies/{id}` | fleet:read | Get one routing policy |
| PUT | `/api/v1/alerts/routing/policies/{id}` | fleet:write | Update a routing policy |
| DELETE | `/api/v1/alerts/routing/policies/{id}` | fleet:write | Delete a routing policy |
| POST | `/api/v1/alerts/routing/resolve` | fleet:read | Resolve routing for a context |

### Escalation Policies

| Method | Endpoint | Permission | Description |
|--------|----------|------------|-------------|
| GET | `/api/v1/alerts/escalation/policies` | fleet:read | List all escalation policies |
| POST | `/api/v1/alerts/escalation/policies` | fleet:write | Create an escalation policy |
| GET | `/api/v1/alerts/escalation/policies/{id}` | fleet:read | Get one escalation policy |
| PUT | `/api/v1/alerts/escalation/policies/{id}` | fleet:write | Update an escalation policy |
| DELETE | `/api/v1/alerts/escalation/policies/{id}` | fleet:write | Delete an escalation policy |

## Integration with Alert Engine

When a `RoutingStore` is attached to the alert engine, each fired/resolved alert is delivered
as a `DeliveredAlertEvent` (additive wrapper around `AlertEvent`) that includes the resolved
`RoutingOutcome`. This enriches webhook payloads and event bus messages with ownership and
runbook context without breaking existing consumers.
