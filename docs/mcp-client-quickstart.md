# MCP Client Quickstart (Claude + OpenClaw)

This guide is the fastest path to connect an MCP client to Legator and verify end-to-end tool invocation.

## Prerequisites

- Legator control-plane reachable at `https://legator.example.com`
- MCP endpoint enabled (default): `https://legator.example.com/mcp`
- API key with at least fleet read permissions (`lgk_...`)

> If auth is disabled in local dev, you can omit the `Authorization` header.

---

## 1) Claude client configuration

Add Legator under `mcpServers`:

```json
{
  "mcpServers": {
    "legator": {
      "url": "https://legator.example.com/mcp",
      "headers": {
        "Authorization": "Bearer lgk_<token>"
      }
    }
  }
}
```

---

## 2) OpenClaw client configuration

OpenClaw uses the same MCP server shape. Add/merge this in your OpenClaw MCP config:

```json
{
  "mcpServers": {
    "legator": {
      "url": "https://legator.example.com/mcp",
      "headers": {
        "Authorization": "Bearer lgk_<token>"
      }
    }
  }
}
```

---

## 3) Verify the golden path

After connecting, run these checks from the client:

1. **List tools**
   - Expected: non-empty tool list
   - Must include at least one Legator tool (for example `legator_list_probes`)

2. **Invoke read tool**
   - Example: `legator_list_probes` with `{ "status": "all" }`

3. **Invoke guarded action**
   - Example: `legator_run_command` with probe id + command
   - Expected outcomes include:
     - command executed, or
     - policy/approval response (`pending`, `denied`, or policy violation)

Any of those guarded outcomes confirms the guarded invoke path is functioning.

---

## 4) Low-level HTTP fallback (debug)

If a client UI is unclear, you can validate transport directly:

```bash
# 1) Open SSE channel (captures /mcp?sessionid=...)
curl -sS -N --http1.1 https://legator.example.com/mcp

# 2) POST JSON-RPC messages to /mcp?sessionid=<id>
#    method=initialize, notifications/initialized, tools/list, tools/call
```

For full request/response transcript examples, see:

- `projects/dev-lab/research/gap-7-mcp-golden-path-e2e-2026-03-06.md`
