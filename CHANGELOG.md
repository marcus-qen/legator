# Changelog

All notable changes to this project will be documented in this file.

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
