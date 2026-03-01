# API + MCP Compatibility & Deprecation Contract (Stage 3.5)

This document defines the compatibility contract for Legator's public control-plane surfaces:

- REST API routes (`/api/v1/*`, plus `/healthz`, `/version`, and MCP transport route `/mcp`)
- MCP tool identifiers (`legator_*`)
- MCP resource URIs (`legator://*`)

## 1) Versioning policy

### REST API

- Stable REST routes live under `/api/v1`.
- Backward-incompatible changes **must not** be introduced in-place under `/api/v1`.
- Breaking REST changes require a new version namespace (for example `/api/v2`) and a migration period.

### MCP tools/resources

- MCP tool names and resource URIs are treated as stable public identifiers once released.
- Breaking MCP identifier changes (rename/remove/semantic repurpose) require explicit deprecation and migration guidance.
- Additive MCP changes (new optional fields, new tools/resources) are allowed.

## 2) Additive vs breaking changes

### Additive (allowed in current major)

- Add new routes/tools/resources.
- Add optional JSON response fields.
- Add optional MCP input fields with safe defaults.
- Add new enum values only when consumers can safely ignore unknown values.

### Breaking (requires versioning + deprecation path)

- Remove or rename stable routes/tools/resources.
- Remove or rename response fields.
- Change field type/meaning in a way that breaks existing clients.
- Tighten validation in ways that reject previously valid requests.

## 3) Deprecation window and removal process

Minimum deprecation window before removal:

- **2 released versions** and
- **30 days**,

whichever is longer.

Removal requires all of the following:

1. Mark deprecation in `docs/contracts/deprecations.json` with:
   - `id`
   - `status` (`deprecated` then `removed`)
   - `deprecated_in`
   - `removal_not_before`
   - `replacement` (or explicit `"none"`)
   - `change_note`
2. Add migration guidance to release notes.
3. Record compatibility annotation in `CHANGELOG.md`.
4. Keep tests green for contract checks.

## 4) CI-enforced contract files

Append-only baseline contracts:

- `docs/contracts/api-v1-stable-routes.txt`
- `docs/contracts/mcp-stable-tools.txt`
- `docs/contracts/mcp-stable-resources.txt`

Deprecation registry:

- `docs/contracts/deprecations.json`

`go test ./internal/controlplane/compat` enforces:

- no silent route/tool/resource removals/renames,
- no untracked additions to stable baselines,
- valid deprecation metadata when removals are declared.

## 5) Required changelog and release-note annotations

All API/MCP surface changes must include a compatibility annotation.

Use one of:

- `[compat:additive]` – additive, backward-compatible surface change.
- `[compat:deprecate]` – starts/updates a deprecation window.
- `[compat:remove]` – removal after deprecation window.

Where to place annotations:

- `CHANGELOG.md` entry under the relevant release section.
- Release notes document under `docs/releases/`.

See `docs/releases/README.md` for the release note template.
