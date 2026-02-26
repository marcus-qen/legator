# Security Model

## Overview

Legator implements defence-in-depth: multiple independent security layers that each prevent or limit damage.

## Layers

1. **Authentication** (who are you?)
   - Local: username/password with bcrypt hashing
   - OIDC: SSO via any OIDC-compliant provider (Keycloak, Auth0, Okta, Google)
   - API keys: for programmatic access
   - All three can be active simultaneously

2. **Authorization** (what can you do?)
   - Role-based: admin, operator, viewer
   - Permission-scoped API keys
   - Roles determine: fleet read/write, command exec, approval decide, audit read, etc.

3. **Policy Engine** (what can the probe do?)
   - Three levels: observe (read-only), diagnose (read + diagnostic), remediate (read + write)
   - Command classifier: safe, elevated, destructive
   - Enforced at BOTH control plane AND probe (dual enforcement)
   - Destructive commands require human approval

4. **Command Signing** (is this command authentic?)
   - HMAC-SHA256 signing of every command
   - Per-probe key derivation from master signing key
   - Probe verifies signature before execution

5. **Audit Trail** (what happened?)
   - Every action logged with timestamp, actor, probe, action, before/after state
   - Immutable SQLite-backed audit log
   - Queryable via API: `GET /api/v1/audit`

## OIDC Configuration

- PKCE (S256) enforced for all OIDC flows
- State parameter validated to prevent CSRF
- Nonce validated to prevent replay
- ID token signature verified against provider JWKS
- No access tokens stored — session is Legator-managed

## Rate Limiting

- Login attempts rate-limited per IP
- API key requests rate-limited per key

## Network

- All probe connections use TLS (WSS)
- Probe authenticates to control plane on connection (API key in initial handshake)
- Control plane never initiates connections to probes

## Secrets Management

- Signing keys: hex-encoded, generated at startup if not set
- Passwords: bcrypt (cost 10)
- API keys: crypto/rand generated, stored hashed
- OIDC client secrets: in config file or env var (0600 permissions recommended)
