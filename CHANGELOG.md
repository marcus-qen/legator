# Changelog

All notable changes to this project will be documented in this file.

## [v0.9.0-rc1] — 2026-02-24

### Theme: Defense in Depth

v0.9.0-rc1 hardens operator workflows across API, CLI, dashboard, and Telegram ChatOps.

### Added

- Telegram ChatOps MVP command surface (`/status`, `/inventory`, `/run`, `/approvals`)
- ChatOps typed-confirmation mutation flow (`/approve`/`/deny` -> `/confirm` with TTL)
- Cross-surface parity probe harness (`hack/p3.3-parity-suite.sh`) with machine-readable checks
- UserPolicy + unified policy evaluator + anomaly baseline pipeline + policy simulation endpoint/CLI

### Changed

- Dashboard approval mutation path now forwards through API authz/safety semantics
- API approvals read path bounded to `agents` namespace with fail-fast timeout
- Manager RBAC extended for `approvalrequests` and `agentstates` resources

### Security / Safety

- Blast radius gate + safety outcomes in audit
- Per-user API rate limiting
- Typed confirmation guardrails on high-risk approval decisions
- Viewer mutation attempts consistently denied across API/CLI/UI/ChatOps parity checks

## [v0.7.0] — 2026-02-21

### Theme: Wire It Up

v0.7.0 closes the gap between "tests pass" and "works in production." Eight orphaned packages were wired into the running controller, deployed, validated, and debugged. Every feature now has production evidence.

### Added

#### Controller Integration (Phase 1)
- Wired `approval.Manager`, `events.Bus`, `state.Manager`, `a2a.Router`, `notify.Router` into `cmd/main.go`
- `NotifyFunc` in `RunConfig` — called after cleanup to send severity-based notifications
- Notification routing via env vars (`LEGATOR_NOTIFY_SLACK_WEBHOOK`, `LEGATOR_NOTIFY_TELEGRAM_TOKEN` + `CHAT_ID`, `LEGATOR_NOTIFY_WEBHOOK_URL`)
- All singletons initialised before `RunConfigFactory` closure

#### Dashboard OIDC (Phase 2)
- Created `legator-dashboard` Keycloak client in `dev-lab` realm
- OIDC middleware wired into dashboard Server struct
- Dashboard redirects to Keycloak, SSO with Pomerium means one login max
- Deployed and accessible at `https://legator.lab.k-dev.uk/`

#### OCI Skill Distribution (Phase 3)
- Skills pushed to Harbor via `legator skill push` with env var auth
- Controller pulls skills from OCI at runtime via `RegistryClient`
- watchman-light running on `oci://harbor.lab.k-dev.uk/legator/skills/endpoint-monitoring:v1`
- Full push → pull → execute → succeed cycle validated

#### State Management (Phase 4.3)
- `AgentState` CRD persisting across agent runs
- watchman-light saves endpoint status map, reads it back on next run
- Agents remember previous findings, skip duplicate alerts, report only changes

#### Webhook Triggers (Phase 5)
- HTTP listener on `:9443` for webhook-triggered agent runs
- `POST /webhook/{source}` routes to agents registered for that source
- AlertManager webhook receiver configured (critical/warning alerts → Legator)
- Debounce (30s window) prevents alert storms from flooding agents
- Reconciler registers webhook triggers on agent create/update
- K8s Service + HTTPRoute expose webhook endpoint

### Fixed

- **Engine not wired to tool registry** — `ClassifiableTool` overrides were dead code since v0.5.0. All tool classifications fell through to heuristic fallback.
- **State tools classified as service mutations** — `state.set`/`state.delete` were `TierServiceMutation`, blocking observe-level agents from using state. Reclassified to `TierRead` (agent's own bookkeeping).
- **A2A tools classified as service mutations** — `a2a.delegate` fell to heuristic default. Added `ClassifiableTool` implementation, classified as `TierRead`.
- **Controller RBAC missing CRD resources** — `agentstates`, `legatorruns`, `approvalrequests`, `agentevents` not in ClusterRole.
- **CRD schema stale** — `approvalMode` and `approvalTimeout` fields missing from cluster CRD. Regenerated manifests.
- **Container entrypoint mismatch** — Dockerfile used `/legator-controller` but deployment expected `/manager`.
- **Reconciler didn't register webhook triggers** — Added `OnReconcile` callback to wire agent triggers to scheduler.

### Changed

- `endpoint-monitoring` skill v1.2.0 — state-aware, reads/writes `run_state` key
- `deployment-verification` skill v2.0.0 — attempts repairs (gated by approval mode)
- `alert-investigation` skill v2.0.0 — delegates to tribune via A2A
- `issue-triage` skill v2.0.0 — processes A2A tasks from other agents

### Infrastructure

- 10/10 agents succeeding on cluster
- 7 CRDs installed (all schemas current)
- Prometheus scraping UP, 4 alert rules active
- AlertManager → Legator webhook integration live
- Harbor hosting OCI skill artifacts

## [v0.6.0] — 2026-02-20

### Theme: Ecosystem & Integration

v0.6.0 connects Legator to the wider world — real registries, real auth, real clouds, and agent-to-agent collaboration.

### Added

#### ORAS Registry Wire-up (Phase 1)
- `RegistryClient`: push/pull skill artifacts to OCI-compliant registries via ORAS v2.6.0
- Push creates proper OCI Image Manifest v1.1 with config + content layers
- Pull extracts content layer, parses config metadata
- Auth: static credentials or LEGATOR_REGISTRY_USERNAME/PASSWORD env vars
- PlainHTTP mode for dev/test registries
- CLI `legator skill push/pull` now works against real registries (ghcr.io, Harbor, etc.)

#### OIDC + Keycloak Authentication (Phase 2)
- Full OIDC middleware for dashboard: .well-known discovery, authorization code flow, session management
- Login → Keycloak redirect → callback → session cookie → dashboard access
- CSRF protection via random state parameter, 10-minute state expiry
- 8-hour sessions, HttpOnly/SameSite cookies
- /healthz bypass (for probes), /static bypass (for assets)
- /auth/login, /auth/callback, /auth/logout routes
- UserFromContext() for RBAC-aware handlers
- **After deployment, your dashboard URL will redirect to Keycloak login**

#### Agent-to-Agent (A2A) Protocol (Phase 3)
- CRD-native task delegation: Agent A → TaskRequest → Agent B
- Router: DelegateTask, GetPendingTasks (priority-sorted), CompleteTask, FailTask, RejectTask
- Priority queue: critical > high > normal > low
- Task lifecycle: pending → accepted → in-progress → completed/failed/rejected/expired
- ExpireOldTasks: TTL-based cleanup of stale pending tasks
- LLM tools: `a2a.delegate` (delegate to another agent), `a2a.check_tasks` (check inbox)
- Uses existing AgentEvent CRDs — no new CRD needed

#### Helm Chart Updates (Phase 4)
- Multi-tenancy: team definitions with namespace + resource quotas
- Notifications: Slack, Telegram, Email, Webhook config with secret refs
- A2A: task routing config (namespace, TTL, scan interval)
- Cloud tools: AWS (region, IRSA) and Azure (subscription, Workload Identity) config
- Dashboard: /healthz health probes (OIDC-safe)

#### AWS + Azure Example Agents (Phase 5)
- 6 example agents: cost-monitor, security-audit, resource-inventory for each provider
- 2 example environments: aws-production, azure-production (Vault + IRSA/Workload Identity)
- All agents at 'observe' autonomy with detailed SKILL.md and report templates
- 12 total agent examples across 4 domains (K8s, SSH, AWS, Azure)

### Stats
- 454 tests across 28 packages
- 8 tool domains: kubectl, HTTP, SSH, SQL, DNS, state, AWS, Azure
- 2 A2A tools: a2a.delegate, a2a.check_tasks
- 12 example agents across 4 infrastructure domains

---

## [v0.5.0] — 2026-02-20

### Theme: Production & Community Readiness

v0.5.0 makes Legator production-grade for teams beyond the dev-lab.

### Added

#### Agent State & Memory (Phase 1)
- New CRD: `AgentState` — persistent key-value storage per agent
- State tools: `state.get`, `state.set`, `state.delete` for LLM access
- TTL per key, quota enforcement (max keys, max value size, 64KB total)
- `FormatContext()` for injecting previous state into agent prompts
- Enables dedup (only report new findings) and multi-step workflows

#### Notification Channels (Phase 2)
- Slack (webhook), Telegram (Bot API), Email (SMTP), generic Webhook
- Severity-based routing: critical→all channels, warning→warning+info, info→info only
- Per-agent per-hour rate limiting (sliding window)
- Markdown escaping for Telegram MarkdownV2

#### OCI Skill Distribution (Phase 3)
- Pack/Unpack: skill directory ↔ OCI artifact layers (tar.gz + JSON config)
- `oci://` reference parsing with tag and digest support
- TTL-based skill cache with filesystem backing
- CLI: `legator skill pack`, `legator skill push`, `legator skill pull`, `legator skill inspect`
- Path traversal protection in Unpack
- ORAS registry integration prepared (layers ready, wire when dependency added)

#### Multi-Tenant Foundations (Phase 4)
- `QuotaEnforcer`: per-team limits on agents, concurrent runs, daily runs, hourly tokens
- Cost attribution: token usage tracked per team with lifetime totals
- Team isolation: quotas enforced independently per team
- Namespace conventions: `legator-<team>` mapping
- Hourly/daily usage reset for quota windows

#### AWS + Azure Cloud Tools (Phase 5)
- `aws.cli`: AWS CLI wrapper with 4-tier command classification
  - 43+ classified commands across EC2, S3, IAM, RDS, Lambda, ECS, DynamoDB
  - S3/DynamoDB deletions always blocked, IAM changes audited, EC2 terminate requires approval
- `az.cli`: Azure CLI wrapper with 4-tier command classification
  - 50+ classified commands across VM, Storage, AKS, SQL, KeyVault, CosmosDB
  - Storage/CosmosDB/KeyVault deletions always blocked, resource group deletion requires approval
- Both tools: 30s timeout, JSON output, 8KB truncation, credential injection via env vars
- 5 built-in protection classes: kubernetes, ssh, sql, aws, azure

### Stats
- 7 tool domains: kubectl, HTTP, SSH, SQL, DNS, AWS, Azure
- 7 CRDs: LegatorAgent, LegatorEnvironment, LegatorRun, ModelTierConfig, ApprovalRequest, AgentEvent, AgentState
- 5 built-in protection classes
- 423 tests across 27 packages

## [v0.4.0] — 2026-02-20

### Theme: Observability & Adoption Readiness

v0.4.0 transforms Legator from a CLI-only operator tool into a full platform with
web dashboard, approval workflows, inter-agent coordination, DNS tooling, and
developer-friendly onboarding.

### Added

#### Web Dashboard (Phase 1)
- Go HTTP server with 8 page routes + 3 htmx live-refresh endpoints
- Dark theme CSS, embedded templates via `//go:embed`
- Pages: agent list, agent detail, runs list, run detail, approvals, events
- Per-page template cloning pattern (avoids Go `{{define "content"}}` collision)
- Separate deployment binary (`cmd/dashboard/main.go`) with signal handling
- Helm chart templates: `dashboard-deployment.yaml`, `dashboard-service.yaml`, `dashboard-rbac.yaml`
- Dashboard ClusterRole: read all Legator CRDs + update ApprovalRequest status
- OIDC flag plumbing (auth enforcement deferred to Keycloak hookup)
- 9 dashboard tests

#### Approval Workflow (Phase 2)
- **New CRD: `ApprovalRequest`** — Pending/Approved/Denied/Expired lifecycle
  - Proposed action details (tool, arguments, tier)
  - Configurable timeout (default 30m)
  - Decision tracking (decidedBy, decidedAt, reason)
- `internal/approval/Manager` — creates ApprovalRequest CRD, polls for decision (5s interval)
- Engine integration: `Decision.NeedsApproval` distinct from hard block
- `GuardrailsSpec.ApprovalMode`: `none` / `mutation-gate` / `plan-first` / `every-action`
- `GuardrailsSpec.ApprovalTimeout`: configurable wait duration
- Runner wiring: three-path tool handling (needs-approval → blocked → execute)
- `ActionStatusApproved`, `ActionStatusDenied`, `ActionStatusPendingApproval` status values
- CLI: `legator approvals` (list with pending-first sort, status icons)
- CLI: `legator approve <name> [reason]` / `legator deny <name> [reason]`
- 6 approval tests

#### Agent Coordination — Event Bus (Phase 3)
- **New CRD: `AgentEvent`** — inter-agent signalling with severity, TTL, consumer tracking
- `internal/events/Bus` — CRD-based event bus (persists through controller restarts)
- `Publish()`: creates AgentEvent with labels for efficient filtering
- `FindNewEvents()`: finds unconsumed events matching criteria (type, source, target, severity)
- `Consume()`: marks events consumed, records triggered runs
- `CleanExpired()`: TTL-based garbage collection
- Severity ordering: info < warning < critical
- Targeted events: only visible to the specified target agent
- Dedup: consumed events never returned to the same agent
- Event lifecycle: New → Consumed → (TTL expiry → deleted)
- 7 event bus tests

#### DNS Tool (Phase 4)
- `dns.query`: A, AAAA, CNAME, MX, TXT, NS lookups
- `dns.reverse`: IP → hostname reverse lookups
- Custom nameserver support for internal DNS servers
- JSON structured output for LLM consumption
- Read-only by design (TierRead, never blocked by classification)
- Implements both `Tool` and `ClassifiableTool` interfaces
- DNS errors returned as result (not failures) — LLM can reason about them
- 10s per-query timeout
- 11 DNS tests

#### Onboarding (Phase 5)
- `legator init` — interactive wizard creates agent scaffold
  - Prompts: name, description, namespace, autonomy, schedule, model tier, tool domain, starter skill
  - 5 starter skill templates: cluster-health, pod-restart-monitor, certificate-expiry, server-health, custom
  - 5 tool domains: kubernetes, ssh, http, sql, dns
  - Generates: agent.yaml, environment.yaml, skill/SKILL.md, skill/actions.yaml
- `legator validate` — pre-deploy validation
  - Checks: YAML syntax, apiVersion, kind, metadata, autonomy level, schedule format, guardrails, skill, environment
  - Emoji-coded output (✅ ❌ ⚠️), exit code 1 on errors
- Helm chart: 3 starter skill ConfigMaps (cluster-health, pod-restart-monitor, certificate-expiry)
- 17 init/validate tests

### Stats
- 5 tool domains: kubectl, HTTP, SSH, SQL, DNS
- 3 connectivity modes: direct, headscale, tailscale
- 3 built-in protection classes: kubernetes, ssh, sql
- 366 tests across 23 packages
- 2 new CRDs (ApprovalRequest, AgentEvent) — total 6

## [v0.3.0] — 2026-02-20

### Added

#### Vault Integration
- `internal/vault/client.go` — HashiCorp Vault API client with K8s auth and token auth
- SSH Certificate Authority signing: short-lived SSH certs (5-min TTL) per agent run
- Dynamic database credentials: Vault creates temporary DB users, auto-revoked on expiry
- KV v2 secret reading for static secrets (API keys, tokens)
- `CredentialManager` with per-run lifecycle: request at start → inject into tool → revoke at end
- LLM never sees credentials — all injection happens at the tool layer
- New credential types in CRD: `vault-kv`, `vault-ssh-ca`, `vault-database`
- `VaultCredentialSpec` and `VaultConfig` CRD types
- `RunConfig.Cleanup` wired into runner for automatic lease revocation + key zeroing
- 17 Vault-specific tests

#### SQL Tool
- `sql.query` tool for read-only database queries (PostgreSQL + MySQL)
- Driver-level read-only enforcement via `sql.TxOptions{ReadOnly: true}`
- Four-tier query classification: SELECT (read) / CREATE INDEX (service) / DROP TABLE (destructive) / INSERT (data)
- SQL injection detection: multi-statement, comment injection, suspicious UNION patterns
- Result truncation: configurable max rows (default 1000) and max bytes (default 8KB)
- `pgx` (PostgreSQL) and `go-sql-driver/mysql` database drivers
- Vault credential injection: `requestVaultDBCredentials()` creates ephemeral DB users per-run
- `buildSQLDatabases()` constructs DSNs from environment credentials
- SQL protection class added to built-in defaults (DELETE, INSERT, UPDATE, DROP all blocked)
- 16 SQL-specific tests
- 3 example agents: `database-health-monitor`, `schema-drift-detector`, `query-performance-auditor`
- Example environment with Vault database credentials

#### Headscale/Tailscale Connectivity
- `ConnectivitySpec` CRD type: `direct`, `headscale`, `tailscale`
- `HeadscaleConnectivity`: control server, auth key, ACL tags, hostname, accept routes, exit node
- `internal/connectivity/` package: health checks, endpoint reachability, pre-run validation
- Tailscale sidecar in Helm chart (optional, disabled by default)
- Userspace mode, shared Unix socket for controller ↔ sidecar communication
- Pre-run connectivity check wired into RunConfigFactory
- `docs/connectivity.md`: architecture, ACLs, subnet routers, troubleshooting
- Example environment (`headscale-environment.yaml`) with Headscale + Vault
- Example agent (`server-health-monitor`) using SSH via Headscale mesh
- 12 connectivity tests

### Changed
- Protection engine now includes SQL protection class as built-in (alongside K8s and SSH)
- Environment resolver exposes `Connectivity` field
- Updated protection engine tests for 4 built-in classes (was 3)
- 409 total tests across 18 packages (was 360 across 17)

## [v0.2.0] — 2026-02-20

### Added

#### SSH Tool
- `ssh.exec` tool for executing commands on remote servers via `golang.org/x/crypto/ssh`
- 150+ commands auto-classified into four action tiers (read/service/destructive/data)
- Blocked command list: `dd`, `mkfs`, `fdisk`, `parted`, `psql`, `mysql`, `mongo`, `mongosh`, `redis-cli`, `shred`, `srm`, `wipefs`
- Protected paths: `/etc/shadow`, `/etc/gshadow`, `/boot/`, `/dev/`, SSH keys
- Per-host sudo and root login controls (opt-in)
- Connection pooling (reuse within a run), 8KB output truncation, configurable timeouts
- Automatic credential injection from LegatorEnvironment secrets
- Subcommand matching (e.g., `mkfs.ext4` → `mkfs` → blocked)

#### Tool Capability Framework
- `ClassifiableTool` interface: tools declare capabilities and classify actions
- `ToolCapability` struct: domain, supported tiers, credential/connection requirements
- `ActionClassification` struct: tier, target, description, blocked status, block reason
- Domain inference from tool names (`kubectl.*` → kubernetes, `ssh.*` → ssh, `mcp.X.*` → X)

#### Protection Engine
- Configurable protection classes with glob-style pattern matching
- `ProtectionClass` / `ProtectionRule` types with block/approve/audit actions
- Built-in Kubernetes protection class (PVC, PV, namespace, DB CR deletion)
- Built-in SSH protection class (shadow file, disk tools, partition tools)
- User-extensible: add protection classes for SQL, HTTP, cloud APIs, etc.
- Built-in rules cannot be weakened by user classes
- Wired into Action Sheet Engine via `WithProtectionEngine()`

#### CLI (`legator` binary)
- `legator agents list` — tabular view of agents with phase, autonomy, schedule
- `legator agents get <name>` — detailed agent view with emoji, description, config
- `legator runs list [--agent NAME]` — recent runs sorted newest-first with emoji status
- `legator runs logs <name>` — full run detail: actions, guardrails, report, errors
- `legator status` — cluster summary: agents, environments, runs, success rate, tokens
- `legator version` — version and git commit info

#### Example Agents
- `legacy-server-scanner` — SSH into servers, parse web configs, produce migration report
- `patch-compliance-checker` — SSH into fleet, audit OS/kernel/packages
- `log-rotation-auditor` — SSH into servers, check logrotate config, disk pressure
- `server-fleet` environment example with SSH credentials

### Changed

#### Renamed from InfraAgent
- Repository: `marcus-qen/infraagent` → `marcus-qen/legator`
- Go module: `github.com/marcus-qen/legator`
- API group: `legator.io/v1alpha1` (from `core.infraagent.io/v1alpha1`)
- CRD kinds: `LegatorAgent`, `LegatorEnvironment`, `LegatorRun` (from `InfraAgent`, etc.)
- Helm chart: `charts/legator/`

#### Guardrail Engine
- `WithProtectionEngine()` — optional configurable protection class evaluation
- `WithToolRegistry()` — optional ClassifiableTool-based action classification
- Protection engine check runs after hardcoded data protection (defence in depth)
- ClassifiableTool blocks fire before engine evaluation (double enforcement)
- `inferDomain()` and `mapToolTierToAPITier()` helper functions

### Fixed
- CiliumNetworkPolicy DNS egress for legator-system namespace
- RBAC kubebuilder markers: resource plural names match CRD plurals

## [v0.1.0] — 2026-02-20

Initial release. See [RELEASE-NOTES-v0.1.0.md](RELEASE-NOTES-v0.1.0.md).

### Features
- Kubernetes operator with 4 CRDs (InfraAgent, InfraAgentEnvironment, InfraAgentRun, ModelTierConfig)
- Scheduled execution (cron, interval, webhook, annotation triggers)
- Four-tier action classification with runtime-enforced guardrails
- Graduated autonomy (observe → recommend → automate-safe → automate-destructive)
- Action Sheet Engine with allowlist enforcement
- Hardcoded data protection (PVC, PV, namespace, DB CRD deletion blocked)
- LLM provider abstraction (Anthropic, OpenAI, any OpenAI-compatible)
- MCP tool integration (Go SDK v1.3.1, Streamable HTTP)
- Skill distribution (Git sources, ConfigMap, bundled)
- Multi-cluster support with remote kubeconfig client factory
- Reporting and escalation (Slack, Telegram, webhook channels)
- 9 Prometheus metrics, OTel tracing, Grafana dashboard
- AgentRun retention with TTL cleanup
- Rate limiting (per-agent + cluster-wide concurrency)
- Credential sanitization in audit trail
- 253 unit tests across 17 packages

### Dogfooding
- 10 agents running on 4-node Talos K8s cluster
- 277+ runs, 82% success rate
- Token usage: ~3K to ~42K per agent per run
