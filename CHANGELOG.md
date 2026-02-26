# Changelog

## v1.0.0-alpha.2 (2026-02-26)

Auth hardening, observability, and public deployment.

### Security
- **Scoped API key permissions** — PermFleetRead, PermFleetWrite, PermCommandExec, ApprovalRead/Write, AuditRead, WebhookManage, Admin
- Auth middleware with skip paths for health/version/probe WS/registration/static
- 401/403 enforcement with permission matrix tests

### Observability
- **Event bus → webhook integration** — all fleet events (connect, disconnect, offline, commands) automatically trigger webhooks
- Webhook forwarder goroutine replaces direct calls; single dispatch path
- **Real-time SSE on probe detail page** — status badge, health, last-seen update live
- WebSocket lifecycle hooks emit probe.connected/probe.disconnected events with status/last_seen payload

### Infrastructure
- **Public Caddy route** — `legator.lab.k-dev.uk` direct to control plane (no Pomerium, no SSH tunnel)
- Probe connects via WSS through public URL

### Testing
- **31 new server package tests** across 3 files (server_test.go, messages_test.go, templates_test.go)
- Template anchor tests ensure SSE wiring survives refactors
- Probe delete + fleet cleanup endpoint tests

### UI
- Chat page context panel with probe system info
- Fleet table chat buttons
- Probe detail incremental DOM updates (no full-page reload)
- Connection indicator badge (live/reconnecting)

### Operations
- Probe delete and fleet cleanup endpoints
- WebSocket keepalive improvements

### Stats
- 94 Go files, 27 test suites, 29 e2e tests
- 10 multi-arch release assets
## v1.0.0-alpha.1 (2026-02-26)

First release of the standalone Legator control plane. Complete rewrite from the K8s-native runtime (v0.1–v0.9.2) to a universal fleet management system.

### Control Plane
- Standalone Go binary (14MB), no K8s dependency
- Web dashboard with dark theme, real-time SSE updates, live activity feed
- 35+ REST API endpoints for fleet management
- WebSocket hub for probe connections
- Persistent SQLite stores: fleet, audit, chat, webhooks, policies, auth
- Config file support (legator.json) + environment variable overrides

### Fleet Management
- Probe registration with single-use tokens
- Real-time health scoring and offline detection
- Tag-based grouping and group command dispatch
- Fleet summary and Prometheus-compatible metrics endpoint

### Security & Policy
- Three capability levels: observe / diagnose / remediate
- Defence in depth: policy enforced at both control plane and probe
- HMAC-SHA256 command signing with per-probe key derivation
- Risk-gated approval queue with auto-expiry
- Multi-user auth with API keys and per-key rate limiting
- API key rotation pushed to probes in real-time
- Credential sanitisation in command output

### Chat & AI
- Per-probe persistent chat sessions (REST + WebSocket)
- LLM integration via any OpenAI-compatible API
- Chat context includes probe inventory and policy level
- LLM-issued commands go through the approval queue

### Operations
- Webhook notifications (probe offline, command failure) with HMAC signing
- Full audit log with filtering by probe, type, time range
- SSE streaming for command output
- Probe self-update with SHA256 checksum verification
- Event bus with SSE endpoint for real-time dashboard

### Probe Agent
- Static Go binary (7MB), zero dependencies
- Systemd service management (install/remove/status)
- System inventory scanner (hostname, CPUs, RAM, services, packages)
- Command execution with streaming output
- Policy enforcement at probe level
- Auto-reconnect with jitter and backoff
- Local health status endpoint
- Config file support with --config-dir flag

### CLI (legatorctl)
- Fleet listing and probe detail
- Multi-arch builds: linux/amd64, linux/arm64, darwin/arm64

### CI/CD
- GitHub Actions: test, build, lint, e2e
- Release workflow: multi-arch binaries + GitHub Release on tag
- One-liner install script with architecture detection

### Stats
- 90 Go files, ~15.7k lines
- 26 test suites, 27 end-to-end tests
- 30 packages
