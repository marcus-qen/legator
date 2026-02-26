# Changelog

## Unreleased

### Control Plane
- **Multi-user auth**: API keys with bcrypt hashing, scoped permissions (admin, fleet:read, command:exec, etc.), opt-in via `LEGATOR_AUTH=true`
- **Per-key rate limiting**: sliding window rate limiter per API key, 429 responses with Retry-After
- **TLS support**: `ListenAndServeTLS` when `LEGATOR_TLS_CERT` and `LEGATOR_TLS_KEY` configured
- **Config file support**: JSON config file with `--config` flag, env var overrides, `init-config` subcommand
- **Persistent webhook storage**: webhooks survive restarts (SQLite `webhook.db`)
- **Persistent policy templates**: custom policy templates survive restarts (SQLite `policy.db`)
- **Binary download endpoint**: `GET /download/{filename}` for probe binary distribution
- **Install script endpoint**: `GET /install.sh` serves the installer
- **Approval queue UI**: web page at `/approvals` with approve/deny buttons, risk badges
- **Audit log UI**: web page at `/audit` with event timeline, type badges
- **Dark theme dashboard**: fleet view with health bars, tag pills, status dots
- **Probe detail page**: dark theme with system info grid, health badges, quick actions
- **Fleet navigation**: header nav links to Fleet, Approvals, Audit
- **Startup logging**: reports TLS, auth, and persistence status

### Probe
- **CLI subcommands**: `probe list`, `probe info <id>`, `probe health <id>` for remote fleet queries
- **legatorctl**: standalone fleet management CLI with `fleet`, `probes`, `probe`, `command`, `tokens`, `keys` subcommands
- **ANSI color output**: status indicators in CLI output
- **JSON output mode**: `--json` flag for machine-readable output

### Infrastructure
- **Multi-arch release workflow**: builds control-plane, probe, and legatorctl for linux/amd64, linux/arm64, darwin/arm64
- **Install script**: `curl|bash` probe deployment with arch detection, checksum verify, systemd service
- **Makefile**: `build-all` target for cross-compilation of all binaries

## v0.9.2 (K8s Era — Archived)

See `archive/k8s-runtime` branch for full history.
