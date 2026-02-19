# ADR-004: Model Tiers, Not Model Names

**Status:** Accepted
**Date:** 2026-02-19

## Context

Agent specs need to declare which LLM to use. Hardcoding model names (e.g. `claude-sonnet-4-20250514`) couples agent specs to providers and makes portability impossible.

## Decision

Agent specs use tiers (`fast`, `standard`, `reasoning`). The ModelTierConfig CRD maps tiers to actual provider/model strings at the cluster level.

## Rationale

- **Portability**: Same agent spec works with Anthropic, OpenAI, or local models
- **Cost control**: Swap models cluster-wide without touching agent specs
- **Right-sizing**: Simple monitoring agents use `fast` (cheap, quick); complex triage uses `reasoning` (expensive, capable)
- **Upgrades**: New model releases are a single ModelTierConfig update

## Consequences

- Agents cannot request a specific model (by design)
- ModelTierConfig is a cluster-level concern, not an agent-level one
- Performance characteristics vary by tier mapping (acceptable: tiers are capability classes, not performance guarantees)
