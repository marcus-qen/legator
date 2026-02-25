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

# 9. Summary
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
