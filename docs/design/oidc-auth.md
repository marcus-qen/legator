# Legator OIDC Authentication — Design Document

**Date:** 2026-02-26
**Status:** Approved (Keith confirmed)
**Target:** v1.0.0-alpha.6

---

## Goal

Add OIDC as an **optional** authentication backend. When configured, users can log in via any OIDC-compliant provider (Keycloak, Auth0, Okta, Google, etc.). Local username/password auth remains the default and always-available fallback.

This is how every serious self-hosted tool works: Grafana, Gitea, Drone, ArgoCD — all offer both local auth AND OIDC.

---

## Design Principles

1. **Zero-config by default** — Legator works with just local auth. OIDC is opt-in.
2. **No new dependencies if possible** — Go stdlib `net/http` + `encoding/json` can handle OAuth2/OIDC flows. Only add `golang.org/x/oauth2` + `github.com/coreos/go-oidc/v3` if the stdlib approach gets painful (it will — JWKS caching, token verification, nonce validation).
3. **Map to existing model** — OIDC users map to Legator's existing User/Role/Permission system. No parallel auth world.
4. **Defence in depth** — OIDC provides authentication (who you are). Legator's RBAC provides authorization (what you can do). They're separate layers.

---

## Configuration

```json
// legator.json
{
  "auth_enabled": true,
  "oidc": {
    "enabled": true,
    "provider_url": "https://keycloak.lab.k-dev.uk/realms/dev-lab",
    "client_id": "legator",
    "client_secret": "...",
    "redirect_url": "https://legator.lab.k-dev.uk/auth/oidc/callback",
    "scopes": ["openid", "email", "profile", "groups"],
    "role_claim": "groups",
    "role_mapping": {
      "platform-admins": "admin",
      "cluster-admins": "admin",
      "developers": "operator",
      "viewers": "viewer"
    },
    "default_role": "viewer",
    "auto_create_users": true
  }
}
```

**Environment variable overrides** (12-factor):
- `LEGATOR_OIDC_ENABLED=true`
- `LEGATOR_OIDC_PROVIDER_URL=https://keycloak.lab.k-dev.uk/realms/dev-lab`
- `LEGATOR_OIDC_CLIENT_ID=legator`
- `LEGATOR_OIDC_CLIENT_SECRET=...`
- `LEGATOR_OIDC_REDIRECT_URL=https://legator.lab.k-dev.uk/auth/oidc/callback`

---

## Auth Flow

### Login Page (when OIDC enabled)

```
┌─────────────────────────────────┐
│        ⚡ Legator Login          │
│                                 │
│  ┌───────────────────────────┐  │
│  │  Sign in with Keycloak    │  │  ← OIDC button (primary)
│  └───────────────────────────┘  │
│                                 │
│  ──────── or ────────           │
│                                 │
│  Username: [_______________]    │  ← Local auth (secondary)
│  Password: [_______________]    │
│  [Sign in]                      │
│                                 │
└─────────────────────────────────┘
```

When OIDC is NOT enabled: current login page unchanged.

### OIDC Flow

```
Browser                    Legator CP              OIDC Provider (Keycloak)
   │                          │                          │
   │ GET /auth/oidc/login     │                          │
   │─────────────────────────→│                          │
   │                          │ generate state + nonce   │
   │                          │ store in session cookie  │
   │  302 → provider/auth     │                          │
   │←─────────────────────────│                          │
   │                          │                          │
   │ GET provider/auth?...    │                          │
   │─────────────────────────────────────────────────────→│
   │                          │                          │
   │ (user authenticates at provider)                    │
   │                          │                          │
   │ 302 → /auth/oidc/callback?code=xxx&state=yyy       │
   │←─────────────────────────────────────────────────────│
   │                          │                          │
   │ GET /auth/oidc/callback  │                          │
   │─────────────────────────→│                          │
   │                          │ exchange code for token  │
   │                          │─────────────────────────→│
   │                          │←─────────────────────────│
   │                          │ verify ID token          │
   │                          │ extract claims           │
   │                          │ map role                 │
   │                          │ create/update user       │
   │                          │ create session           │
   │  Set-Cookie + 302 → /    │                          │
   │←─────────────────────────│                          │
```

### Claim → User Mapping

1. **Subject claim** (`sub`) → User ID (prefixed: `oidc:sub-value`)
2. **Preferred username** (`preferred_username`) or **email** → Username
3. **Name** (`name`) or **display_name** → Display name
4. **Groups/roles claim** (configurable via `role_claim`) → matched against `role_mapping` → Legator role

If `auto_create_users` is true (default), first OIDC login auto-creates the user in `users.db`. If false, user must be pre-provisioned.

### Role Resolution Priority

1. If user has explicit role in `users.db` → use that (admin override)
2. If OIDC claims match `role_mapping` → use highest matching role
3. Otherwise → `default_role` (default: `viewer`)

---

## New Package: `internal/controlplane/oidc/`

```go
// Provider handles OIDC authentication.
type Provider struct {
    config     Config
    verifier   *oidc.IDTokenVerifier
    oauth2     oauth2.Config
    logger     *zap.Logger
}

// Config for OIDC authentication.
type Config struct {
    Enabled       bool              `json:"enabled"`
    ProviderURL   string            `json:"provider_url"`
    ClientID      string            `json:"client_id"`
    ClientSecret  string            `json:"client_secret"`
    RedirectURL   string            `json:"redirect_url"`
    Scopes        []string          `json:"scopes"`
    RoleClaim     string            `json:"role_claim"`
    RoleMapping   map[string]string `json:"role_mapping"`
    DefaultRole   string            `json:"default_role"`
    AutoCreate    bool              `json:"auto_create_users"`
}

// HandleLogin redirects to the OIDC provider's authorization endpoint.
func (p *Provider) HandleLogin(w http.ResponseWriter, r *http.Request)

// HandleCallback processes the OIDC callback, exchanges code, creates session.
func (p *Provider) HandleCallback(userStore UserStore, sessionCreator SessionCreator) http.HandlerFunc
```

---

## New Routes

| Route | Method | Purpose |
|---|---|---|
| `/auth/oidc/login` | GET | Redirect to OIDC provider |
| `/auth/oidc/callback` | GET | Process OIDC callback |

Both added to `skipPaths` in auth middleware.

---

## Dependencies

```
github.com/coreos/go-oidc/v3  — OIDC discovery, JWKS, ID token verification
golang.org/x/oauth2           — OAuth2 flow (authorization code grant)
```

These are standard, well-maintained, widely-used. The `go-oidc` library handles provider discovery (`.well-known/openid-configuration`), JWKS key caching, and ID token validation. Rolling our own would be a security risk.

---

## Login Page Changes

The `login.html` template gets a conditional OIDC section:

```html
{{if .OIDCEnabled}}
<a href="/auth/oidc/login" class="oidc-btn">
  Sign in with {{.OIDCProviderName}}
</a>
<div class="divider">or</div>
{{end}}
<!-- existing username/password form -->
```

`OIDCProviderName` is derived from the provider URL or configurable (`oidc.provider_name`).

---

## Security Considerations

1. **State parameter** — random, stored in httpOnly cookie, validated on callback. Prevents CSRF.
2. **Nonce** — included in auth request, verified in ID token. Prevents replay.
3. **PKCE** — use S256 code challenge. Prevents authorization code interception.
4. **Token validation** — ID token signature verified against provider JWKS. Expiry checked. Issuer and audience validated.
5. **No access token storage** — we only use the ID token claims. Access token is discarded after exchange. We don't call the provider's APIs.
6. **Redirect URL validation** — hardcoded in config, not user-controlled.

---

## Testing

1. **Unit tests** — mock OIDC provider (httptest server with JWKS endpoint), test full flow
2. **Integration test** — test against our Keycloak instance (manual, post-deploy)
3. **e2e test** — config with OIDC disabled → existing auth still works (regression)

---

## Our Deployment (dev-lab)

Keycloak client to create:
- **Realm:** `dev-lab`
- **Client ID:** `legator`
- **Client type:** Confidential (authorization code flow)
- **Valid redirect URIs:** `https://legator.lab.k-dev.uk/auth/oidc/callback`
- **Scopes:** openid, email, profile, groups
- **Groups claim:** mapped via client scope or protocol mapper

Role mapping:
- `platform-admins` → `admin`
- `cluster-admins` → `admin`
- `developers` → `operator`
- `viewers` → `viewer`

---

## What Doesn't Change

- API key auth (Bearer lgk_...) — unchanged
- Probe WebSocket auth (ProbeAuthenticator) — unchanged
- RBAC permission model — unchanged
- Local user/password auth — unchanged (always available)
- Session cookies — unchanged (OIDC creates the same session)

---

*The beauty of this design: once authenticated via OIDC, the user looks exactly like a local user to every other part of the system. One session model, one permission model, one audit trail.*
