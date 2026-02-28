# Multi-User RBAC Design

## Overview
Add user accounts with role-based access to the web UI and API, coexisting with the existing API key system.

## Auth Model
Two authentication paths, same permission system:
- **API keys** → programmatic access (probes, scripts, CI) — existing, unchanged
- **User sessions** → web UI access (humans in browsers) — new

## Components

### 1. User Store (`internal/controlplane/users/`)
- SQLite-backed user accounts
- Fields: id, username, display_name, password_hash (bcrypt), role, enabled, created_at, last_login
- CRUD: Create, Get, List, Update, Delete, Authenticate
- Default admin user created on first run if no users exist

### 2. Session Store (`internal/controlplane/session/`)
- SQLite-backed sessions
- Fields: id (token), user_id, created_at, expires_at, last_active
- Create, Validate, Refresh, Delete, Cleanup (expired)
- Session token: secure random hex, stored in HTTP-only cookie
- Session lifetime: 24h, refresh on activity

### 3. Roles → Permissions
| Role | Permissions |
|------|------------|
| admin | All (PermAdmin) |
| operator | FleetRead, FleetWrite, CommandExec, ApprovalRead, ApprovalWrite, AuditRead, WebhookManage |
| viewer | FleetRead, ApprovalRead, AuditRead |

### 4. Middleware Changes
Current flow: Bearer token → KeyStore.Validate → APIKey in context
New flow:
1. Check `Authorization: Bearer lgk_...` → API key path (existing)
2. Check `legator_session` cookie → session path (new)
3. Neither → 401 (API) or redirect to /login (web pages)

Both paths produce the same context value: user identity + permissions.

### 5. Web UI
- `GET /login` → login form
- `POST /login` → authenticate, set session cookie, redirect to /
- `POST /logout` → clear session, redirect to /login
- `GET /api/v1/me` → current user info
- Navigation bar shows username + logout button
- Web pages require session auth (not API key)

### 6. User Management API
- `GET /api/v1/users` — list users (admin only)
- `POST /api/v1/users` — create user (admin only)
- `GET /api/v1/users/{id}` — get user (admin only)
- `PUT /api/v1/users/{id}` — update user (admin only, or self for password)
- `DELETE /api/v1/users/{id}` — delete user (admin only, not self)

### 7. First-Run Bootstrap
If no users exist when the server starts:
- Generate random admin password
- Create "admin" user with admin role
- Print credentials to stdout/log (once)
- User changes password on first login

## Non-Goals (v1)
- OAuth/OIDC integration (future)
- Password reset email (no email system)
- MFA/2FA (future)
- User groups (roles are sufficient for now)

## File Layout
```
internal/controlplane/users/
  store.go       — SQLite user store
  store_test.go  — unit tests
internal/controlplane/session/
  store.go       — SQLite session store
  store_test.go  — unit tests
internal/controlplane/auth/
  middleware.go   — extended: session + API key dual path
  roles.go        — role → permission mapping
web/templates/
  login.html      — login page
```

## Alpha.13 RBAC Hardening Notes

- Enforced write-scope parity for mutating APIs:
  - `POST /api/v1/discovery/scan`
  - `POST /api/v1/discovery/install-token`
  - `POST|PUT|DELETE /api/v1/model-profiles` and `POST /api/v1/model-profiles/{id}/activate`
  - `POST|PUT|DELETE /api/v1/cloud/connectors` and `POST /api/v1/cloud/connectors/{id}/scan`
  - `POST /api/v1/kubeflow/actions/refresh` (read-only routes stay `fleet:read`)
- Tightened page-level auth checks:
  - `/approvals` requires `approval:read`
  - `/audit` requires `audit:read`
- UI now mirrors backend authorization:
  - Sidebar hides routes user cannot read
  - Approvals/Alerts/Model Dock/Cloud Connectors/Discovery write actions are disabled or hidden without write permissions
- Authorization denials now emit explicit audit events (`auth.authorization_denied`) with safe metadata only (`method`, `path`, `required_permission`, `reason`).
