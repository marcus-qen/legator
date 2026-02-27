#!/usr/bin/env bash
# End-to-end test: control plane + probe on localhost
set -uo pipefail

PORT=19090
CP_URL="http://localhost:$PORT"
PROBE_CONFIG_DIR=$(mktemp -d)
DATA_DIR=$(mktemp -d)
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
  rm -rf "$DATA_DIR"
}
trap cleanup EXIT

echo "=== Legator E2E Test ==="
echo ""

# 1. Start control plane
echo "1. Starting control plane on :$PORT..."
LEGATOR_LISTEN_ADDR=":$PORT" LEGATOR_DATA_DIR="$DATA_DIR" ./bin/control-plane &
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


# 20. Tag management
echo ""
echo "20. Tag management..."
TAG_RESULT=$(curl -sf -X PUT "$CP_URL/api/v1/probes/$PROBE_ID/tags"   -H "Content-Type: application/json"   -d '{"tags":["prod","web","linux"]}')
TAG_COUNT=$(echo "$TAG_RESULT" | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('tags',[])))" 2>/dev/null)
if [[ "$TAG_COUNT" == "3" ]]; then
  pass "Tags set (3 tags)"
else
  fail "Tag set failed (got count=$TAG_COUNT)"
fi

# 21. Fleet tags endpoint
echo ""
echo "21. Fleet tags endpoint..."
FLEET_TAGS=$(curl -sf "$CP_URL/api/v1/fleet/tags")
PROD_COUNT=$(echo "$FLEET_TAGS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('tags',{}).get('prod',0))" 2>/dev/null)
if [[ "$PROD_COUNT" -ge 1 ]]; then
  pass "Fleet tags lists prod=$PROD_COUNT"
else
  fail "Fleet tags missing prod count"
fi

# 22. List by tag
echo ""
echo "22. List by tag..."
BY_TAG=$(curl -sf "$CP_URL/api/v1/fleet/by-tag/prod")
BY_TAG_COUNT=$(echo "$BY_TAG" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null)
if [[ "$BY_TAG_COUNT" -ge 1 ]]; then
  pass "List by tag returns $BY_TAG_COUNT probes"
else
  fail "List by tag empty"
fi

# 23. Policy templates
echo ""
echo "23. Policy template listing..."
POLICIES=$(curl -sf "$CP_URL/api/v1/policies")
POL_COUNT=$(echo "$POLICIES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null)
if [[ "$POL_COUNT" -ge 3 ]]; then
  pass "Policy store has $POL_COUNT templates (3 built-in)"
else
  fail "Expected at least 3 policy templates, got $POL_COUNT"
fi

# 24. Apply policy to probe
echo ""
echo "24. Apply policy to probe..."
APPLY=$(curl -sf -X POST "$CP_URL/api/v1/probes/$PROBE_ID/apply-policy/diagnose")
APPLY_STATUS=$(echo "$APPLY" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null)
if [[ "$APPLY_STATUS" == "applied" ]]; then
  pass "Policy 'diagnose' applied to probe"
else
  fail "Policy apply returned status=$APPLY_STATUS (expected applied)"
fi

# 25. Verify probe policy level changed
echo ""
echo "25. Verify probe policy level..."
PROBE_LEVEL=$(curl -sf "$CP_URL/api/v1/probes/$PROBE_ID" | python3 -c "import sys,json; print(json.load(sys.stdin).get('policy_level',''))" 2>/dev/null)
if [[ "$PROBE_LEVEL" == "diagnose" ]]; then
  pass "Probe policy level is now diagnose"
else
  fail "Probe policy level is $PROBE_LEVEL (expected diagnose)"
fi



# 26. Probe health endpoint
echo ""
echo "26. Probe health endpoint..."
HEALTH=$(curl -sf "$CP_URL/api/v1/probes/$PROBE_ID/health")
HEALTH_SCORE=$(echo "$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('score', -1))" 2>/dev/null || echo "-1")
HEALTH_STATUS=$(echo "$HEALTH" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status', ''))" 2>/dev/null || echo "")
if [[ "$HEALTH_SCORE" -ge 0 ]]; then
  pass "Health endpoint returns score=$HEALTH_SCORE status=$HEALTH_STATUS"
else
  fail "Health endpoint failed: $HEALTH"
fi

# 27. Prometheus metrics endpoint
echo ""
echo "27. Prometheus metrics endpoint..."
METRICS=$(curl -sf "$CP_URL/api/v1/metrics")
if echo "$METRICS" | grep -q "legator_probes_registered"; then
  pass "Metrics endpoint returns Prometheus format"
  METRIC_PROBES=$(echo "$METRICS" | grep "legator_probes_registered " | awk '{print $2}')
  echo "  Registered probes: $METRIC_PROBES"
else
  fail "Metrics endpoint missing expected metrics"
fi

# 27. Metrics include websocket connections
echo ""
echo "28. Metrics include websocket connections..."
if echo "$METRICS" | grep -q "legator_websocket_connections"; then
  pass "Metrics include websocket connections"
else
  fail "Metrics missing websocket connections"
fi

# 29. Summary

# 29. Delete probe endpoint
echo ""
echo "29. Delete probe endpoint..."
# Register a temp probe to delete
DELETE_TOKEN=$(curl -s -X POST "$CP_URL/api/v1/tokens" | jq -r '.token // empty')
if [[ -n "$DELETE_TOKEN" ]]; then
  DELETE_REG=$(curl -s -X POST "$CP_URL/api/v1/register" \
    -H "Content-Type: application/json" \
    -d "{\"token\": \"$DELETE_TOKEN\", \"hostname\": \"delete-test\", \"os\": \"linux\", \"arch\": \"amd64\"}")
  DELETE_ID=$(echo "$DELETE_REG" | jq -r '.probe_id // empty')
  if [[ -n "$DELETE_ID" ]]; then
    DEL_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$CP_URL/api/v1/probes/$DELETE_ID")
    if [[ "$DEL_RESP" == "200" ]]; then
      # Verify it's gone
      GET_RESP=$(curl -s -o /dev/null -w "%{http_code}" "$CP_URL/api/v1/probes/$DELETE_ID")
      if [[ "$GET_RESP" == "404" ]]; then
        pass "Delete probe removes probe from fleet"
      else
        fail "Deleted probe still accessible (status=$GET_RESP)"
      fi
    else
      fail "Delete probe returned $DEL_RESP"
    fi
  else
    fail "Could not register temp probe for delete test"
  fi
else
  fail "Could not generate token for delete test"
fi

# 30. Fleet cleanup endpoint
echo ""
echo "30. Fleet cleanup endpoint..."
CLEANUP_RESP=$(curl -s -X POST "$CP_URL/api/v1/fleet/cleanup?older_than=0s")
CLEANUP_COUNT=$(echo "$CLEANUP_RESP" | jq -r '.count // 0')
if echo "$CLEANUP_RESP" | jq -e '.removed' > /dev/null 2>&1; then
  pass "Fleet cleanup returns removed list (cleaned $CLEANUP_COUNT)"
else
  fail "Fleet cleanup response invalid"
fi

# 30b. Fleet summary endpoint (legacy summary placeholder backfill)
echo ""
echo "30b. Fleet summary endpoint..."
SUMMARY_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$CP_URL/api/v1/fleet/summary")
if [[ "$SUMMARY_CODE" == "200" ]]; then
  pass "Fleet summary endpoint returns 200"
else
  fail "Fleet summary endpoint returned $SUMMARY_CODE"
fi

# 31. Model Dock: Create profile
echo ""
echo "31. Model Dock: Create profile..."
MODEL_CREATE=$(curl -sf -X POST "$CP_URL/api/v1/model-profiles" \
  -H "Content-Type: application/json" \
  -d '{"name":"test-profile","provider":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-test","model":"gpt-4o-mini"}')
MODEL_PROFILE_ID=$(echo "$MODEL_CREATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('profile',{}).get('id',''))" 2>/dev/null || echo "")
if [[ -n "$MODEL_PROFILE_ID" ]]; then
  pass "Model profile created (id=$MODEL_PROFILE_ID)"
else
  fail "Model profile create failed: $MODEL_CREATE"
fi

# 32. Model Dock: List profiles
echo ""
echo "32. Model Dock: List profiles..."
MODEL_LIST=$(curl -sf "$CP_URL/api/v1/model-profiles")
if echo "$MODEL_LIST" | python3 -c "import sys,json; profiles=json.load(sys.stdin).get('profiles',[]); assert any(p.get('id')=='$MODEL_PROFILE_ID' and p.get('name')=='test-profile' for p in profiles)" 2>/dev/null; then
  pass "Model profile visible in list"
else
  fail "Model profile missing from list: $MODEL_LIST"
fi

# 33. Model Dock: Activate profile
echo ""
echo "33. Model Dock: Activate profile..."
MODEL_ACTIVATE=$(curl -sf -X POST "$CP_URL/api/v1/model-profiles/$MODEL_PROFILE_ID/activate")
MODEL_ACTIVATE_STATUS=$(echo "$MODEL_ACTIVATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")
if [[ "$MODEL_ACTIVATE_STATUS" == "activated" ]]; then
  pass "Model profile activated"
else
  fail "Model profile activate failed: $MODEL_ACTIVATE"
fi

# 34. Model Dock: Get active profile
echo ""
echo "34. Model Dock: Get active profile..."
MODEL_ACTIVE=$(curl -sf "$CP_URL/api/v1/model-profiles/active")
MODEL_ACTIVE_ID=$(echo "$MODEL_ACTIVE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('profile',{}).get('id',''))" 2>/dev/null || echo "")
if [[ "$MODEL_ACTIVE_ID" == "$MODEL_PROFILE_ID" ]]; then
  pass "Active profile matches activated profile"
else
  fail "Active profile mismatch: $MODEL_ACTIVE"
fi

# 35. Model Dock: Usage endpoint
echo ""
echo "35. Model Dock: Usage endpoint..."
MODEL_USAGE=$(curl -sf "$CP_URL/api/v1/model-usage")
if echo "$MODEL_USAGE" | python3 -c "import sys,json; d=json.load(sys.stdin); assert isinstance(d.get('usage', []), list)" 2>/dev/null; then
  pass "Model usage endpoint returns usage array"
else
  fail "Model usage response invalid: $MODEL_USAGE"
fi

# 36. Model Dock: Delete profile
echo ""
echo "36. Model Dock: Delete profile..."
# Active profiles require another active profile (or env fallback) before delete.
MODEL_FALLBACK_CREATE=$(curl -sf -X POST "$CP_URL/api/v1/model-profiles" \
  -H "Content-Type: application/json" \
  -d '{"name":"test-profile-fallback","provider":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-test-2","model":"gpt-4o-mini"}')
MODEL_FALLBACK_ID=$(echo "$MODEL_FALLBACK_CREATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('profile',{}).get('id',''))" 2>/dev/null || echo "")
if [[ -n "$MODEL_FALLBACK_ID" ]]; then
  curl -sf -X POST "$CP_URL/api/v1/model-profiles/$MODEL_FALLBACK_ID/activate" > /dev/null
fi
MODEL_DELETE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$CP_URL/api/v1/model-profiles/$MODEL_PROFILE_ID")
if [[ "$MODEL_DELETE_CODE" == "200" || "$MODEL_DELETE_CODE" == "204" ]]; then
  pass "Model profile deleted"
else
  fail "Model profile delete returned $MODEL_DELETE_CODE"
fi

# 37. Cloud Connectors: Create connector
echo ""
echo "37. Cloud Connectors: Create connector..."
CLOUD_CREATE=$(curl -sf -X POST "$CP_URL/api/v1/cloud/connectors" \
  -H "Content-Type: application/json" \
  -d '{"name":"test-aws","provider":"aws","config":{"region":"us-east-1","access_key_id":"AKIATEST","secret_access_key":"testkey"}}')
CLOUD_CONNECTOR_ID=$(echo "$CLOUD_CREATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('connector',{}).get('id',''))" 2>/dev/null || echo "")
if [[ -n "$CLOUD_CONNECTOR_ID" ]]; then
  pass "Cloud connector created (id=$CLOUD_CONNECTOR_ID)"
else
  fail "Cloud connector create failed: $CLOUD_CREATE"
fi

# 38. Cloud Connectors: List connectors
echo ""
echo "38. Cloud Connectors: List connectors..."
CLOUD_LIST=$(curl -sf "$CP_URL/api/v1/cloud/connectors")
if echo "$CLOUD_LIST" | python3 -c "import sys,json; connectors=json.load(sys.stdin).get('connectors',[]); assert any(c.get('id')=='$CLOUD_CONNECTOR_ID' and c.get('name')=='test-aws' for c in connectors)" 2>/dev/null; then
  pass "Cloud connector visible in list"
else
  fail "Cloud connector missing from list: $CLOUD_LIST"
fi

# 39. Cloud Connectors: List assets (empty)
echo ""
echo "39. Cloud Connectors: List assets (empty)..."
CLOUD_ASSETS=$(curl -sf "$CP_URL/api/v1/cloud/assets")
if echo "$CLOUD_ASSETS" | python3 -c "import sys,json; d=json.load(sys.stdin); assert isinstance(d.get('assets', []), list)" 2>/dev/null; then
  pass "Cloud assets endpoint returns array"
else
  fail "Cloud assets response invalid: $CLOUD_ASSETS"
fi

# 40. Cloud Connectors: Delete connector
echo ""
echo "40. Cloud Connectors: Delete connector..."
CLOUD_DELETE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$CP_URL/api/v1/cloud/connectors/$CLOUD_CONNECTOR_ID")
if [[ "$CLOUD_DELETE_CODE" == "200" || "$CLOUD_DELETE_CODE" == "204" ]]; then
  pass "Cloud connector deleted"
else
  fail "Cloud connector delete returned $CLOUD_DELETE_CODE"
fi

# 41. Discovery: List runs (empty)
echo ""
echo "41. Discovery: List runs (empty)..."
DISCOVERY_RUNS=$(curl -sf "$CP_URL/api/v1/discovery/runs")
RUN_COUNT=$(echo "$DISCOVERY_RUNS" | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('runs', [])))" 2>/dev/null || echo "-1")
if [[ "$RUN_COUNT" == "0" ]]; then
  pass "Discovery runs list is empty before scan"
else
  fail "Expected 0 discovery runs before scan, got $RUN_COUNT"
fi

# 42. Discovery: Scan endpoint exists
echo ""
echo "42. Discovery: Scan endpoint exists..."
DISCOVERY_SCAN_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$CP_URL/api/v1/discovery/scan" \
  -H "Content-Type: application/json" \
  -d '{"cidr":"192.168.1.0/24","scan_type":"ping"}')
if [[ "$DISCOVERY_SCAN_CODE" == "200" || "$DISCOVERY_SCAN_CODE" == "202" ]]; then
  pass "Discovery scan endpoint accepted request (status=$DISCOVERY_SCAN_CODE)"
else
  fail "Discovery scan endpoint returned $DISCOVERY_SCAN_CODE"
fi

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
