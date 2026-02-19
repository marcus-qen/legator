# ADR-003: Action Sheets as Allowlists

**Status:** Accepted
**Date:** 2026-02-19

## Context

Skills define what an agent can do. We needed a mechanism to constrain tool calls to declared actions only.

## Decision

Action Sheets (`actions.yaml`) act as allowlists. Any tool call not matching a declared action is denied.

## Rationale

- **Explicit > implicit**: Every action the agent can take is documented and reviewed
- **Audit-friendly**: Security teams can review Action Sheets without reading LLM prompts
- **Composable**: Skills declare their actions; the runtime enforces them
- **Defence in depth**: Even if the LLM hallucinates a dangerous tool call, it won't match an action and will be blocked

### Alternative considered: Deny-only lists

A deny-only approach (block specific actions, allow everything else) was rejected because it requires anticipating every dangerous action. Allowlists fail safe — an unanticipated action is blocked by default.

## Consequences

- Skill authors must declare every action upfront
- New tool calls require updating the Action Sheet
- Overly restrictive Action Sheets reduce agent effectiveness (but this is preferable to overly permissive ones)
