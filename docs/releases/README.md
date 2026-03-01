# Release Notes Contract

Release notes in `docs/releases/` must include compatibility metadata for API/MCP surface changes.

## Required section for every release

```markdown
## Compatibility
- [compat:additive] ...
- [compat:deprecate] ...
- [compat:remove] ...
```

Only include tags that apply to that release. If there are no API/MCP changes, state explicitly:

```markdown
## Compatibility
- No API/MCP surface changes.
```

## Tag meanings

- `[compat:additive]` — additive and backward-compatible route/tool/resource change.
- `[compat:deprecate]` — deprecation announcement or active deprecation-window update.
- `[compat:remove]` — removal after deprecation window; must reference deprecation record.

## Mandatory cross-checks

When a release changes API/MCP surfaces:

1. Update `CHANGELOG.md` with compatibility annotation(s).
2. Update baselines in `docs/contracts/*.txt` when adding stable surfaces.
3. Update `docs/contracts/deprecations.json` when deprecating/removing surfaces.
4. Ensure `go test ./internal/controlplane/compat` passes.

Policy reference: `docs/api-mcp-compatibility.md`.
