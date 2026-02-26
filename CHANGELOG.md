# Changelog

All notable changes to Legator are documented here.

## [v1.0.0-alpha.6] — 2026-02-26

### Added
- OIDC authentication (optional SSO via Keycloak, Auth0, Okta, Google, etc.)
- Consistent JSON error responses with error codes across all API endpoints
- Graceful LLM-down handling (user-friendly message instead of 500)
- WebSocket resilience (malformed JSON survived, connections not dropped)
- Dark UI with centered chat layout (Claude Desktop-inspired)
- Warm colour palette and typography polish
- Chat slide-over context panel (hidden by default)
- Textarea input with auto-resize, Enter to send
- Empty state with "Ask this probe anything" prompt
- Configuration reference documentation
- Security model documentation
- This changelog

### Fixed
- Chat history race condition (history skipped when WebSocket connected first)
- WebSocket "Disconnected" indicator showing before connection attempted
- Context panel excessive vertical space
- Responsive breakpoint too aggressive (changed from 1180px to 900px)
- Tailwind CDN removed from production (replaced with hand-written utility classes)

## [v1.0.0-alpha.5] — 2026-02-26

### Added
- Token lifecycle hardening with list tokens API
- Command classifier with defence-in-depth policy enforcement
- Install script hardening with SHA256 verification
- Request-derived install commands in registration response
- Policy persistence across probe restarts
- README rewrite, getting-started guide, and architecture documentation

## [v1.0.0-alpha.4] — 2026-02-26

### Added
- Multi-user RBAC (admin, operator, viewer roles)
- Login UI with session-based authentication
- User management API (create, list, delete)
- Probe WebSocket authentication (API key verification)
- Multi-user RBAC design document

## [v1.0.0-alpha.3] — 2026-02-26

### Added
- Build/version injection hardening with Makefile
- Webhook delivery metrics and diagnostics endpoint
- Incremental SSE updates on probe detail page
- Deployment and upgrade guide

## [v1.0.0-alpha.2] — 2026-02-26

### Added
- Real-time SSE updates on probe detail page
- Webhook notifier (wired to event bus)
- Scoped API key permissions on all routes
- Server package unit tests (31 tests)
- Chat page with probe context panel
- Probe delete and fleet cleanup endpoints
- WebSocket keepalive and LLM chat integration

## [v1.0.0-alpha.1] — 2026-02-26

### Added
- Standalone Go control plane (no Kubernetes dependency)
- Probe agent (systemd service, WebSocket connection, heartbeat)
- Fleet management (register, list, health scoring, tags)
- Command dispatch with HMAC-SHA256 signing
- Output streaming (SSE)
- LLM-powered chat per probe
- Policy engine (observe/diagnose/remediate)
- Approval queue with risk classification
- Audit log (SQLite, immutable)
- Web UI (fleet dashboard, probe detail, chat)
- REST API (35+ endpoints)
- Prometheus metrics
- Event bus (pub/sub)
- CI/CD (test, build, lint, e2e, multi-arch release)
- Install script for one-liner probe deployment
