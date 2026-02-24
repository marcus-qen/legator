# Legator v0.9.0 (Release Candidate)

**Theme:** Defense in Depth

v0.9.0 hardens human operations by making safety decisions explicit, testable, and consistent across API, CLI, dashboard, and Telegram ChatOps.

## Highlights

### P1 — Core safety primitives
- Blast-radius disclosure contract and execution gate
- Per-user API rate limiting with metrics
- Typed confirmation flow for high-tier actions
- Safety gate outcomes recorded in audit trail

### P2 — Policy intelligence + anomaly baseline
- UserPolicy CRD scaffold
- Unified RBAC + UserPolicy evaluator
- Anomaly baseline detection pipeline
- Policy simulation endpoint + CLI flow

### P3 — ChatOps + parity
- Telegram-first ChatOps MVP (status, inventory, run, approvals)
- Typed confirmation + timeout guardrails for chat mutations
- Cross-surface parity suite with machine-readable verdict
- Dashboard approval action path now API-forwarded to preserve authz/safety semantics

## Verification summary

- Target package suites pass (API, ChatOps, dashboard, CLI command surfaces)
- Live parity artifact reports all checks true and `pass: true`
- Live approvals-read runtime issue was root-caused to RBAC and fixed/deployed with green re-validation

## Candidate status

- **v0.9.0-rc1:** Ready
- **Final tag:** pending explicit approver GO
