# Contributing to Legator

This project enforces architecture guardrails as an explicit contributor preflight.

## Fast path before every push

```bash
# Fast-fail architecture checks
make architecture-guard GO=go

# Recommended contributor preflight (guardrails + full tests)
make preflight GO=go
```

## CI fast-fail contract

CI runs a dedicated preflight guardrails job first. If architecture checks fail, build/lint/e2e jobs do not run.

Guardrail references:

- Contract: `docs/contracts/architecture-boundaries.yaml`
- Baseline lock: `docs/contracts/architecture-cross-boundary-imports.txt`
- Exception registry: `docs/contracts/architecture-boundary-exceptions.yaml`
- Guide: `docs/architecture/ci-boundary-guardrails.md`

## Intentional boundary exceptions (escalation path)

If you must keep a temporary cross-boundary dependency:

1. Add/update the allow rule in `dependency_policy.allow` with rationale.
2. Add/update an exception in `docs/contracts/architecture-boundary-exceptions.yaml`.
3. Include reviewer sign-off, tracking issue, approved date, expiry date, and explicit removal expectation.
4. Update changelog/release note with rationale and removal plan.

Exceptions without reviewer sign-off or expiry are rejected by guardrail tests.

## Baseline refresh (intentional drift only)

```bash
LEGATOR_UPDATE_ARCH_IMPORT_BASELINE=1 go test ./internal/controlplane/compat -run TestBoundaryContract_ImportGraphBaselineLock -count=1
```

Only run this after architecture review and include rationale in the PR/changelog.
