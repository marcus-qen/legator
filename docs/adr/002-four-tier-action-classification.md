# ADR-002: Four-Tier Action Classification

**Status:** Accepted
**Date:** 2026-02-19

## Context

LLM-powered agents need guardrails. We needed a system to classify the risk of every tool call and enforce appropriate restrictions.

## Decision

Four-tier classification: `read → service-mutation → destructive-mutation → data-mutation`.

Data mutations are unconditionally blocked. No configuration can override this.

## Rationale

- **read**: Zero risk. Always permitted at any autonomy level.
- **service-mutation**: Recoverable disruption. Safe to automate with guardrails (cooldowns, pre-conditions).
- **destructive-mutation**: Irreversible service changes. Requires explicit opt-in via `automate-destructive`.
- **data-mutation**: Irreversible data loss. Cost of false negative (allowing deletion) vastly exceeds cost of false positive (blocking a safe operation). Therefore: always blocked.

### Why not three tiers?

Separating "destructive service changes" from "data destruction" lets operators confidently set `automate-destructive` for agents that need to clean up old deployments without risking databases. The fourth tier exists specifically to make `automate-destructive` safe.

## Consequences

- Agents cannot automate database maintenance (backup rotation, vacuum, etc.) — this requires human action or a separate, non-InfraAgent tool
- The hardcoded rules must be maintained as new resource types emerge
- The classification is deliberately conservative — false positives are expected and acceptable
