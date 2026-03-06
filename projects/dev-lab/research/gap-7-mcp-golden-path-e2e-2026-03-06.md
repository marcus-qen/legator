# GAP-7 Evidence — MCP Golden-Path E2E (2026-03-06)

## Scope

Acceptance criteria to prove:

1. Tools list is non-empty on a fresh environment.
2. Invoke path works for at least two tools (one read + one guarded action).
3. Quickstart documentation is published for Claude/OpenClaw client connection.

---

## Environment + approach

- Repo: `/tmp/legator-pivot`
- Branch: `agent/gap7-mcp-goldenpath-e2e`
- Binary under test: `./bin/control-plane` and `./bin/probe`
- Fresh runtime state used (`/tmp/legator-gap7-20260306-161732/...`)
- Validation performed over MCP transport (`GET/POST /mcp`) with JSON-RPC messages.

---

## Exact command transcript (fresh environment)

```bash
set -euo pipefail
TS=$(date +%Y%m%d-%H%M%S)
BASE=/tmp/legator-gap7-$TS
DATA_DIR="$BASE/data"
PROBE_DIR="$BASE/probe"
mkdir -p "$DATA_DIR" "$PROBE_DIR"

CP_LOG="$BASE/control-plane.log"
PROBE_LOG="$BASE/probe.log"
SSE_LOG="$BASE/mcp-sse.log"

LEGATOR_LISTEN_ADDR=127.0.0.1:18080 LEGATOR_DATA_DIR="$DATA_DIR" ./bin/control-plane >"$CP_LOG" 2>&1 &
CP_PID=$!

for i in $(seq 1 50); do
  if curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
curl -sS http://127.0.0.1:18080/healthz

TOKEN_JSON=$(curl -sS -X POST http://127.0.0.1:18080/api/v1/tokens)
echo "$TOKEN_JSON"
TOKEN=$(printf '%s' "$TOKEN_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')

./bin/probe init --config-dir "$PROBE_DIR" --server http://127.0.0.1:18080 --token "$TOKEN"
PROBE_ID=$(python3 - <<PY
import yaml
cfg=yaml.safe_load(open('$PROBE_DIR/config.yaml'))
print(cfg['probe_id'])
PY
)

echo "probe_id=$PROBE_ID"
./bin/probe run --config-dir "$PROBE_DIR" >"$PROBE_LOG" 2>&1 &
PROBE_PID=$!

for i in $(seq 1 50); do
  STATUS=$(curl -sS http://127.0.0.1:18080/api/v1/probes | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["status"] if d else "none")' || true)
  if [ "$STATUS" = "online" ]; then
    break
  fi
  sleep 0.2
done
curl -sS http://127.0.0.1:18080/api/v1/probes

curl -sS -N --http1.1 http://127.0.0.1:18080/mcp > "$SSE_LOG" &
SSE_PID=$!
for i in $(seq 1 50); do
  if grep -q 'sessionid=' "$SSE_LOG" 2>/dev/null; then
    break
  fi
  sleep 0.1
done
SESSION=$(awk -F'sessionid=' '/sessionid=/{print $2}' "$SSE_LOG" | tr -d '\r' | head -n1)

echo "session=$SESSION"

post(){
  local payload="$1"
  curl -sS -i -X POST "http://127.0.0.1:18080/mcp?sessionid=$SESSION" \
    -H 'Content-Type: application/json' \
    --data "$payload" | sed -n '1,20p'
}

post '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"gap7-e2e","version":"1.0"}}}'
post '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
post '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
post '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"legator_list_probes","arguments":{"status":"all"}}}'
post '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"legator_run_command","arguments":{"probe_id":"'"$PROBE_ID"'","command":"echo GAP7_GUARDED"}}}'

sleep 2
cat "$SSE_LOG"

kill $SSE_PID $PROBE_PID $CP_PID >/dev/null 2>&1 || true
wait $SSE_PID $PROBE_PID $CP_PID >/dev/null 2>&1 || true
```

---

## Key API responses (from transcript)

### Probe registration + online confirmation

```json
{"token":"prb_5279280e-84c_1772813853_78a49b2e62679081", ...}
```

```json
[{"id":"prb-e99d857d","hostname":"principia","os":"linux","arch":"amd64","status":"online","policy_level":"observe", ...}]
```

### MCP session established

```text
event: endpoint
data: /mcp?sessionid=JMZAHG4GAGLZAOADQ3DOJM3IXF
```

### Tools list is non-empty

`tools/list` response included 8 tools, including:

- `legator_list_probes`
- `legator_probe_info`
- `legator_run_command`
- `legator_get_inventory`
- `legator_fleet_query`
- `legator_search_audit`
- `legator_probe_health`
- `legator_decide_approval`

### Read tool invocation works

```json
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"[{\"id\":\"prb-e99d857d\",\"hostname\":\"principia\",\"status\":\"online\",\"last_seen\":\"2026-03-06T16:17:33.326062913Z\"}]"}]}}
```

### Guarded tool invocation works (policy gate exercised)

```json
{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"exit_code=-1\npolicy violation: command classified as remediate but probe is at observe level"}]}}
```

This is a valid guarded-path result: tool call reached command dispatch logic and returned policy enforcement output.

---

## Acceptance verdict

- ✅ **Tools list non-empty on fresh environment**
- ✅ **Invoke path works for read tool (`legator_list_probes`)**
- ✅ **Invoke path works for guarded tool (`legator_run_command`) with policy-gated response**
- ✅ **Quickstart doc published for Claude/OpenClaw**

---

## Gap found (non-blocking) + reproducible steps

### Observation

Bundled `./bin/control-plane` in this environment returns `404` for the additive route `/api/v1/mcp/tools` (while MCP `/mcp` works and was validated above).

### Repro

```bash
BASE=/tmp/legator-gap7-routecheck-$(date +%Y%m%d-%H%M%S)
mkdir -p "$BASE/data"
LEGATOR_LISTEN_ADDR=127.0.0.1:18080 LEGATOR_DATA_DIR="$BASE/data" ./bin/control-plane >"$BASE/cp.log" 2>&1 &
PID=$!
for i in $(seq 1 40); do curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1 && break; sleep 0.1; done
curl -sS -o /tmp/mcp-tools-route.out -w "%{http_code}\n" http://127.0.0.1:18080/api/v1/mcp/tools
head -n 5 /tmp/mcp-tools-route.out
kill $PID >/dev/null 2>&1 || true
```

Output:

```text
404
404 page not found
```

### Proposed issue text

**Title:** Bundled `bin/control-plane` artifact is stale vs source: `/api/v1/mcp/tools` route returns 404

**Body:**
- On fresh start of `./bin/control-plane` from repo artifact, `GET /api/v1/mcp/tools` returns 404.
- Source tree includes MCP client handler wiring, but bundled binary appears older.
- This can confuse E2E validation because MCP transport (`/mcp`) works while additive MCP REST helper routes do not.
- Recommendation: refresh committed build artifacts (or stop committing binaries) and add artifact/source version check in CI.
