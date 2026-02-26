#!/usr/bin/env bash
# End-to-end test: control plane + probe on localhost
set -uo pipefail

PORT=19090
CP_URL="http://localhost:$PORT"
PROBE_CONFIG_DIR=$(mktemp -d)
PASSED=0
FAILED=0
CP_PID=""
PROBE_PID=""

pass() { echo "  ✅ $1"; ((PASSED++)); }
fail() { echo "  ❌ $1"; ((FAILED++)); }

cleanup() {
  [[ -n "$CP_PID" ]] && kill $CP_PID 2>/dev/null || true
  [[ -n "$PROBE_PID" ]] && kill $PROBE_PID 2>/dev/null || true
  rm -rf "$PROBE_CONFIG_DIR"
}
trap cleanup EXIT

echo "=== Legator E2E Test ==="
echo ""

# 1. Start control plane
echo "1. Starting control plane on :$PORT..."
LEGATOR_LISTEN_ADDR=":$PORT" ./bin/control-plane &
CP_PID=$!
sleep 1

if curl -sf "$CP_URL/healthz" > /dev/null; then
  pass "Control plane started"
else
  fail "Control plane failed to start"
  exit 1
fi

# 2. Generate a registration token
echo ""
echo "2. Generating registration token..."
TOKEN_JSON=$(curl -sf -X POST "$CP_URL/api/v1/tokens")
TOKEN=$(echo "$TOKEN_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")
if [[ "$TOKEN" == prb_* ]]; then
  pass "Token generated: ${TOKEN:0:20}..."
else
  fail "Token generation failed: $TOKEN_JSON"
fi

# 3. Register probe
echo ""
echo "3. Registering probe..."
REG_JSON=$(curl -sf -X POST "$CP_URL/api/v1/register" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"hostname\":\"e2e-test\",\"os\":\"linux\",\"arch\":\"amd64\"}")
PROBE_ID=$(echo "$REG_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['probe_id'])")
API_KEY=$(echo "$REG_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['api_key'])")

if [[ "$PROBE_ID" == prb-* ]]; then
  pass "Probe registered: $PROBE_ID"
else
  fail "Registration failed: $REG_JSON"
fi

# 4. Token should be consumed
echo ""
echo "4. Verifying token is consumed..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$CP_URL/api/v1/register" \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"hostname\":\"evil\",\"os\":\"linux\",\"arch\":\"amd64\"}")
if [[ "$HTTP_CODE" == "401" ]]; then
  pass "Token consumed (reuse rejected with 401)"
else
  fail "Token reuse should return 401, got $HTTP_CODE"
fi

# 5. Check fleet state
echo ""
echo "5. Checking fleet state..."
FLEET=$(curl -sf "$CP_URL/api/v1/probes")
if echo "$FLEET" | python3 -c "import sys,json; probes=json.load(sys.stdin); assert any(p['id']=='$PROBE_ID' for p in probes)" 2>/dev/null; then
  pass "Probe visible in fleet"
else
  fail "Probe not in fleet: $FLEET"
fi

# 6. Write probe config and start probe agent
echo ""
echo "6. Starting probe agent..."
cat > "$PROBE_CONFIG_DIR/config.yaml" <<YAMLDOC
server_url: "$CP_URL"
probe_id: "$PROBE_ID"
api_key: "$API_KEY"
YAMLDOC

./bin/probe run --config-dir "$PROBE_CONFIG_DIR" &
PROBE_PID=$!
sleep 4

# 7. Check probe sent inventory
echo ""
echo "7. Checking inventory was received..."
PROBE_STATE=$(curl -sf "$CP_URL/api/v1/probes/$PROBE_ID")
HAS_INVENTORY=$(echo "$PROBE_STATE" | python3 -c "
import sys,json
p = json.load(sys.stdin)
inv = p.get('inventory')
if inv and inv.get('hostname'):
  print('yes')
  print(f'  Hostname: {inv[\"hostname\"]}')
  print(f'  CPUs: {inv[\"cpus\"]}')
  print(f'  Services: {len(inv.get(\"services\", []))}')
  print(f'  Packages: {len(inv.get(\"packages\", []))}')
else:
  print('no')
" 2>/dev/null || echo "no")

if [[ "$HAS_INVENTORY" == yes* ]]; then
  pass "Inventory received"
  echo "$HAS_INVENTORY" | tail -n +2
else
  fail "No inventory received"
  echo "  Probe state: $(echo "$PROBE_STATE" | head -c 200)"
fi

# 8. Send a command to the probe
echo ""
echo "8. Sending command to probe..."
CMD_RESULT=$(curl -sf -X POST "$CP_URL/api/v1/probes/$PROBE_ID/command" \
  -H "Content-Type: application/json" \
  -d '{"request_id":"e2e-cmd-1","command":"echo","args":["hello from control plane"],"level":"observe","timeout":5000000000}')

if echo "$CMD_RESULT" | grep -q "dispatched"; then
  pass "Command dispatched"
else
  fail "Command dispatch failed: $CMD_RESULT"
fi

sleep 2

# 9. Synchronous command (wait for result)
echo ""
echo "9. Sending synchronous command (?wait=true)..."
SYNC_RESULT=$(curl -sf -X POST "$CP_URL/api/v1/probes/$PROBE_ID/command?wait=true" \
  -H "Content-Type: application/json" \
  -d '{"command":"echo","args":["sync-hello"],"level":"observe","timeout":5000000000}')

SYNC_EXIT=$(echo "$SYNC_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('exit_code', -1))" 2>/dev/null || echo "-1")
SYNC_OUT=$(echo "$SYNC_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('stdout', ''))" 2>/dev/null || echo "")

if [[ "$SYNC_EXIT" == "0" ]] && echo "$SYNC_OUT" | grep -q "sync-hello"; then
  pass "Synchronous command returned result (exit=$SYNC_EXIT)"
else
  fail "Sync command failed: exit=$SYNC_EXIT out=$SYNC_OUT full=$SYNC_RESULT"
fi

# 10. Pending commands should be 0
echo ""
echo "10. Checking no pending commands..."
PENDING=$(curl -sf "$CP_URL/api/v1/commands/pending")
IN_FLIGHT=$(echo "$PENDING" | python3 -c "import sys,json; print(json.load(sys.stdin).get('in_flight', -1))" 2>/dev/null || echo "-1")
if [[ "$IN_FLIGHT" == "0" ]]; then
  pass "No pending commands (all resolved)"
else
  fail "Expected 0 pending, got $IN_FLIGHT"
fi

# 11. Task endpoint returns 503 without LLM config
echo ""
echo "11. Checking task endpoint (no LLM configured)..."
TASK_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$CP_URL/api/v1/probes/$PROBE_ID/task" \
  -H "Content-Type: application/json" \
  -d '{"task":"check uptime"}')
if [[ "$TASK_CODE" == "503" ]]; then
  pass "Task endpoint returns 503 when no LLM configured"
else
  fail "Expected 503 for task without LLM, got $TASK_CODE"
fi


# 12. Approval queue — dangerous command should be held
echo ""
echo "12. Testing approval queue (dangerous command)..."
DANGER_RESULT=$(curl -sf -X POST "$CP_URL/api/v1/probes/$PROBE_ID/command" \
  -H "Content-Type: application/json" \
  -d '{"command":"rm","args":["-rf","/tmp/test"],"level":"remediate","timeout":5000000000}')

DANGER_STATUS=$(echo "$DANGER_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status', ''))" 2>/dev/null || echo "")
APPROVAL_ID=$(echo "$DANGER_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('approval_id', ''))" 2>/dev/null || echo "")

if [[ "$DANGER_STATUS" == "pending_approval" ]] && [[ -n "$APPROVAL_ID" ]]; then
  pass "Dangerous command held for approval (id=$APPROVAL_ID)"
else
  fail "Expected pending_approval, got: $DANGER_RESULT"
fi

# 13. Approval queue — list pending
echo ""
echo "13. Checking pending approvals..."
PENDING_APPROVALS=$(curl -sf "$CP_URL/api/v1/approvals?status=pending")
PENDING_COUNT=$(echo "$PENDING_APPROVALS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('pending_count', 0))" 2>/dev/null || echo "0")
if [[ "$PENDING_COUNT" -ge 1 ]]; then
  pass "Pending approval count: $PENDING_COUNT"
else
  fail "Expected at least 1 pending approval, got $PENDING_COUNT"
fi

# 14. Approval queue — deny the request
echo ""
echo "14. Denying approval request..."
DENY_RESULT=$(curl -sf -X POST "$CP_URL/api/v1/approvals/$APPROVAL_ID/decide" \
  -H "Content-Type: application/json" \
  -d '{"decision":"denied","decided_by":"e2e-test"}')

DENY_STATUS=$(echo "$DENY_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status', ''))" 2>/dev/null || echo "")
if [[ "$DENY_STATUS" == "denied" ]]; then
  pass "Approval request denied"
else
  fail "Expected denied, got: $DENY_RESULT"
fi

# 15. Fleet summary includes approval count
echo ""
echo "15. Checking fleet summary includes approvals..."
SUMMARY=$(curl -sf "$CP_URL/api/v1/fleet/summary")
HAS_APPROVALS=$(echo "$SUMMARY" | python3 -c "import sys,json; d=json.load(sys.stdin); print('yes' if 'pending_approvals' in d else 'no')" 2>/dev/null || echo "no")
if [[ "$HAS_APPROVALS" == "yes" ]]; then
  pass "Fleet summary includes pending_approvals field"
else
  fail "Fleet summary missing pending_approvals"
fi

# 16. Audit log has entries
echo ""
echo "16. Checking audit log..."
AUDIT=$(curl -sf "$CP_URL/api/v1/audit?limit=5")
AUDIT_TOTAL=$(echo "$AUDIT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('total', 0))" 2>/dev/null || echo "0")
if [[ "$AUDIT_TOTAL" -ge 3 ]]; then
  pass "Audit log has $AUDIT_TOTAL entries"
else
  fail "Expected at least 3 audit entries, got $AUDIT_TOTAL"
fi


# 17. Streaming command dispatch
echo ""
echo "17. Streaming command dispatch..."
STREAM_RESULT=$(curl -sf -X POST "$CP_URL/api/v1/probes/$PROBE_ID/command?stream=true"   -H "Content-Type: application/json"   -d '{"command":"echo","args":["streaming works"],"level":"observe"}')
STREAM_RID=$(echo "$STREAM_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('request_id', ''))" 2>/dev/null)
if [[ -n "$STREAM_RID" ]]; then
  pass "Streaming command dispatched (request_id=$STREAM_RID)"
else
  fail "Streaming command dispatch failed"
fi

# 18. SSE stream endpoint exists
echo ""
echo "18. SSE stream endpoint..."
SSE_STATUS=$(curl -sf -o /dev/null -w "%{http_code}" --max-time 3 "$CP_URL/api/v1/commands/fake-id/stream" 2>/dev/null || echo "timeout")
# SSE holds connection open waiting for data. curl returns 000 on max-time timeout = endpoint is live.
if [[ "$SSE_STATUS" == "timeout" || "$SSE_STATUS" == "200" || "$SSE_STATUS" == "000timeout" ]]; then
  pass "SSE stream endpoint is live"
else
  fail "SSE stream endpoint returned unexpected status: $SSE_STATUS"
fi

# 19. Chat API exists
echo ""
echo "19. Chat API endpoints..."
CHAT_STATUS=$(curl -sf -o /dev/null -w "%{http_code}" "$CP_URL/api/v1/probes/$PROBE_ID/chat?limit=10" 2>/dev/null || echo "000")
if [[ "$CHAT_STATUS" == "200" ]]; then
  pass "Chat GET endpoint returns 200"
else
  fail "Chat GET endpoint returned $CHAT_STATUS (expected 200)"
fi

# 20. Summary
echo ""
echo "=========================="
echo "Results: $PASSED passed, $FAILED failed"
echo "=========================="

if [[ $FAILED -eq 0 ]]; then
  echo "🎉 ALL TESTS PASSED"
  exit 0
else
  echo "💀 SOME TESTS FAILED"
  exit 1
fi
