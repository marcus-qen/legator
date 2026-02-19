# CRD Reference

Complete field reference for all InfraAgent Custom Resource Definitions.

## InfraAgent

**API Group:** `core.infraagent.io/v1alpha1`
**Scope:** Namespaced

Defines an autonomous infrastructure agent — its identity, schedule, model configuration, skills, capabilities, guardrails, and environment binding.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `description` | string | ✅ | — | Human-readable summary of what this agent does |
| `emoji` | string | | — | Icon for human-friendly display |
| `schedule` | [ScheduleSpec](#schedulespec) | ✅ | — | When the agent runs |
| `model` | [ModelSpec](#modelspec) | ✅ | — | LLM tier and budget |
| `skills` | [][SkillRef](#skillref) | ✅ | — | Skills to load (min 1) |
| `capabilities` | [CapabilitiesSpec](#capabilitiesspec) | | — | Required/optional tool capabilities |
| `guardrails` | [GuardrailsSpec](#guardrailsspec) | ✅ | — | Safety boundaries and escalation |
| `observability` | [ObservabilitySpec](#observabilityspec) | | metrics+tracing on | Telemetry configuration |
| `reporting` | [ReportingSpec](#reportingspec) | | success=silent | Run outcome actions |
| `environmentRef` | string | ✅ | — | Name of AgentEnvironment to bind |
| `paused` | bool | | false | Stops scheduling without deleting |

### ScheduleSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cron` | string | — | Standard cron expression (e.g. `*/5 * * * *`) |
| `interval` | string | — | Alternative to cron (e.g. `5m`, `300s`) |
| `timezone` | string | `UTC` | IANA timezone for cron evaluation |
| `triggers` | [][TriggerSpec](#triggerspec) | — | Event-driven execution |

### TriggerSpec

| Field | Type | Description |
|-------|------|-------------|
| `type` | enum | `webhook` or `kubernetes-event` |
| `source` | string | Event origin (e.g. `alertmanager`) |
| `filter` | string | CEL expression for event matching |
| `resources` | []string | K8s resource kinds to watch |
| `reasons` | []string | Event reasons to match |

### ModelSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tier` | enum | `standard` | Model class: `fast`, `standard`, `reasoning` |
| `tokenBudget` | int64 | 50000 | Hard max tokens per run |
| `timeout` | string | `120s` | Max wall-clock duration per run |

### SkillRef

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Skill identifier |
| `source` | string | Where the skill lives: `bundled`, `configmap`, or `git://host/org/repo#path@ref` |

### CapabilitiesSpec

| Field | Type | Description |
|-------|------|-------------|
| `required` | []string | Must be satisfiable by the environment |
| `optional` | []string | Used if available |

### GuardrailsSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `autonomy` | enum | `observe` | `observe`, `recommend`, `automate-safe`, `automate-destructive` |
| `allowedActions` | []string | — | Glob list of permitted tool calls |
| `deniedActions` | []string | — | Always-blocked (overrides allow) |
| `escalation` | [EscalationSpec](#escalationspec) | — | Autonomy-ceiling event handling |
| `maxIterations` | int32 | 10 | Hard limit on tool-call loops |
| `maxRetries` | int32 | 2 | Retries on transient failure |

### EscalationSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `target` | enum | — | `parent`, `channel`, `human` |
| `channelName` | string | — | Named channel in AgentEnvironment |
| `timeout` | string | `300s` | Wait time for response |
| `onTimeout` | enum | `cancel` | `cancel`, `proceed`, `retry` |

### ObservabilitySpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `metrics` | bool | true | Prometheus metric emission |
| `tracing` | bool | true | OpenTelemetry spans |
| `logLevel` | enum | `info` | `debug`, `info`, `warn`, `error` |

### ReportingSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `onSuccess` | enum | `silent` | `silent`, `log`, `notify`, `escalate` |
| `onFailure` | enum | `escalate` | Action on failed run |
| `onFinding` | enum | `log` | Action on noteworthy discovery |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | enum | `Pending`, `Ready`, `Running`, `Error`, `Paused` |
| `lastRunTime` | time | When the agent last executed |
| `nextRunTime` | time | Computed next execution time |
| `runCount` | int64 | Total runs |
| `consecutiveFailures` | int32 | Sequential failures (for alerting) |
| `lastRunName` | string | Name of most recent AgentRun |
| `conditions` | []Condition | Standard K8s conditions |

---

## AgentEnvironment

**API Group:** `core.infraagent.io/v1alpha1`
**Scope:** Namespaced

Site-specific configuration — endpoints, credentials, namespaces, channels, data resources, and MCP servers.

### Spec

| Field | Type | Description |
|-------|------|-------------|
| `connection` | [ConnectionSpec](#connectionspec) | How to connect to the target cluster |
| `endpoints` | map[string][EndpointSpec](#endpointspec) | Named service endpoints |
| `namespaces` | [NamespaceMap](#namespacemap) | Namespaces grouped by role |
| `credentials` | map[string][CredentialRef](#credentialref) | Named credential references |
| `channels` | map[string][ChannelSpec](#channelspec) | Notification channels |
| `dataResources` | [DataResourcesSpec](#dataresourcesspec) | Declared data resources |
| `mcpServers` | map[string][MCPServerSpec](#mcpserverspec) | MCP tool servers |

### ConnectionSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `kind` | enum | `in-cluster` | `in-cluster` or `kubeconfig` |
| `kubeconfig` | [KubeconfigRef](#kubeconfigref) | — | Secret containing kubeconfig (when kind=kubeconfig) |

### KubeconfigRef

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `secretRef` | string | — | Secret name in agent's namespace |
| `key` | string | `kubeconfig` | Data key within the Secret |

### EndpointSpec

| Field | Type | Description |
|-------|------|-------------|
| `url` | string | Base URL |
| `healthPath` | string | Appended to URL for health checks |
| `internal` | bool | Cluster-internal (no TLS required) |

### NamespaceMap

| Field | Type | Description |
|-------|------|-------------|
| `monitoring` | []string | Monitoring namespaces |
| `apps` | []string | Application namespaces |
| `system` | []string | Platform/infrastructure namespaces |
| `additional` | map[string][]string | Arbitrary groupings |

### CredentialRef

| Field | Type | Description |
|-------|------|-------------|
| `secretRef` | string | Secret name |
| `type` | enum | `bearer-token`, `token`, `api-key`, `basic-auth`, `tls` |

### ChannelSpec

| Field | Type | Description |
|-------|------|-------------|
| `type` | enum | `slack`, `telegram`, `webhook`, `agent` |
| `target` | string | Destination (webhook URL, chat ID, agent name) |
| `secretRef` | string | Auth credentials (optional) |

### DataResourcesSpec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backupMaxAge` | string | `24h` | Max acceptable time since last backup |
| `databases` | [][DataResourceRef](#dataresourceref) | — | Database cluster CRs |
| `persistentStorage` | [][DataResourceRef](#dataresourceref) | — | PVCs and PVs |
| `objectStorage` | [][ObjectStorageRef](#objectstorageref) | — | S3/MinIO buckets |

### DataResourceRef

| Field | Type | Description |
|-------|------|-------------|
| `kind` | string | Resource kind (e.g. `cnpg.io/Cluster`) |
| `namespace` | string | Resource namespace |
| `name` | string | Resource name |
| `backupSchedule` | string | Informational — for freshness checks |

### ObjectStorageRef

| Field | Type | Description |
|-------|------|-------------|
| `kind` | string | Storage type (e.g. `s3-bucket`) |
| `name` | string | Bucket name |
| `provider` | string | Provider (e.g. `minio`, `aws`) |

### MCPServerSpec

| Field | Type | Description |
|-------|------|-------------|
| `endpoint` | string | MCP server URL |
| `capabilities` | []string | Tool capabilities provided |

---

## ModelTierConfig

**API Group:** `core.infraagent.io/v1alpha1`
**Scope:** Cluster

Maps tier names to provider/model strings and auth configuration.

### Spec

| Field | Type | Description |
|-------|------|-------------|
| `defaultAuth` | [AuthSpec](#authspec) | Default auth for all tiers |
| `tiers` | [][TierMapping](#tiermapping) | Tier → provider/model mappings |

### AuthSpec

| Field | Type | Description |
|-------|------|-------------|
| `type` | enum | `apiKey`, `oauth`, `serviceAccount`, `none`, `custom` |
| `secretRef` | string | Secret name |
| `secretKey` | string | Key within the Secret |

### TierMapping

| Field | Type | Description |
|-------|------|-------------|
| `tier` | enum | `fast`, `standard`, `reasoning` |
| `provider` | string | Provider name (e.g. `anthropic`, `openai`) |
| `model` | string | Model identifier |
| `maxTokens` | int32 | Max tokens for this tier |
| `costPerMillionInput` | string | USD per 1M input tokens |
| `costPerMillionOutput` | string | USD per 1M output tokens |
| `auth` | [AuthSpec](#authspec) | Override auth for this tier |

---

## AgentRun

**API Group:** `core.infraagent.io/v1alpha1`
**Scope:** Namespaced

Immutable audit record of a single agent execution. Once phase reaches a terminal state, no field is ever modified.

### Spec

| Field | Type | Description |
|-------|------|-------------|
| `agentRef` | string | Owning InfraAgent name |
| `environmentRef` | string | AgentEnvironment used |
| `trigger` | enum | `scheduled`, `webhook`, `manual` |
| `modelUsed` | string | Resolved provider/model string |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | enum | `Pending`, `Running`, `Succeeded`, `Failed`, `Escalated`, `Blocked` |
| `startTime` | time | Run start |
| `completionTime` | time | Run end |
| `usage` | [UsageSummary](#usagesummary) | Resource consumption |
| `actions` | [][ActionRecord](#actionrecord) | Ordered tool call audit trail |
| `guardrails` | [GuardrailSummary](#guardrailsummary) | Guardrail activity summary |
| `findings` | [][RunFinding](#runfinding) | Noteworthy discoveries |
| `report` | string | Agent's human-readable summary |
| `conditions` | []Condition | Standard K8s conditions |

### ActionRecord

| Field | Type | Description |
|-------|------|-------------|
| `seq` | int32 | Sequence number |
| `timestamp` | time | When attempted |
| `tool` | string | Tool identifier (e.g. `kubectl.get`) |
| `target` | string | What was acted on |
| `tier` | enum | Risk classification |
| `preFlightCheck` | PreFlightResult | Safety check results |
| `result` | string | Tool output (sanitized, truncated) |
| `status` | enum | `executed`, `blocked`, `failed`, `skipped` |
| `escalation` | ActionEscalation | Escalation details (if blocked) |

### UsageSummary

| Field | Type | Description |
|-------|------|-------------|
| `tokensIn` | int64 | Input tokens |
| `tokensOut` | int64 | Output tokens |
| `totalTokens` | int64 | Total |
| `iterations` | int32 | Tool-call loops |
| `wallClockMs` | int64 | Duration |
| `estimatedCost` | string | USD estimate |
