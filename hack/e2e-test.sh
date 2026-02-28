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

# 2b. Generate a non-expiring multi-use token and register with it
echo ""
echo "2b. Generating non-expiring multi-use token..."
TOKEN_JSON_NO_EXPIRY=$(curl -sf -X POST "$CP_URL/api/v1/tokens?multi_use=true&no_expiry=true")
TOKEN_NO_EXPIRY=$(echo "$TOKEN_JSON_NO_EXPIRY" | python3 -c "import sys,json; print(json.load(sys.stdin).get(\"token\", \"\"))")
if [[ "$TOKEN_NO_EXPIRY" == prb_* ]]; then
  pass "Non-expiring token generated: ${TOKEN_NO_EXPIRY:0:20}..."

  REG_NO_EXPIRY_JSON=$(curl -sf -X POST "$CP_URL/api/v1/register" \
    -H "Content-Type: application/json" \
    -d "{\"token\":\"$TOKEN_NO_EXPIRY\",\"hostname\":\"e2e-test-no-expiry\",\"os\":\"linux\",\"arch\":\"amd64\"}")
  NO_EXPIRY_PROBE_ID=$(echo "$REG_NO_EXPIRY_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get(\"probe_id\", \"\"))")
  NO_EXPIRY_API_KEY=$(echo "$REG_NO_EXPIRY_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get(\"api_key\", \"\"))")

  if [[ -n "$NO_EXPIRY_PROBE_ID" && "$NO_EXPIRY_PROBE_ID" == prb-* ]]; then
    pass "Non-expiring token used for registration: $NO_EXPIRY_PROBE_ID"
  else
    fail "Non-expiring token registration failed: $REG_NO_EXPIRY_JSON"
  fi
else
  fail "Non-expiring token generation failed: $TOKEN_JSON_NO_EXPIRY"
fi

# 2c. Re-register same hostname should deduplicate to one probe
echo ""
echo "2c. Verifying re-registration deduplicates by hostname..."
if [[ "$TOKEN_NO_EXPIRY" == prb_* ]]; then
  REREG_JSON=$(curl -sf -X POST "$CP_URL/api/v1/register" \
    -H "Content-Type: application/json" \
    -d "{\"token\":\"$TOKEN_NO_EXPIRY\",\"hostname\":\"e2e-test-no-expiry\",\"os\":\"linux\",\"arch\":\"arm64\",\"tags\":[\"e2e\",\"dedup\"]}")
  REREG_PROBE_ID=$(echo "$REREG_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get(\"probe_id\", \"\"))")
  REREG_API_KEY=$(echo "$REREG_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get(\"api_key\", \"\"))")

  if [[ "$REREG_PROBE_ID" == "$NO_EXPIRY_PROBE_ID" ]]; then
    pass "Re-registration reused existing probe ID"
  else
    fail "Expected re-registration to reuse $NO_EXPIRY_PROBE_ID, got $REREG_PROBE_ID"
  fi

  if [[ -n "$NO_EXPIRY_API_KEY" && "$REREG_API_KEY" != "$NO_EXPIRY_API_KEY" ]]; then
    pass "Re-registration rotated API key"
  else
    fail "Expected API key rotation on re-registration"
  fi

  DEDUP_COUNT=$(curl -sf "$CP_URL/api/v1/probes" | python3 -c "import sys,json; probes=json.load(sys.stdin); print(sum(1 for p in probes if p.get('hostname') == 'e2e-test-no-expiry'))")
  if [[ "$DEDUP_COUNT" == "1" ]]; then
    pass "Fleet has a single entry for re-registered hostname"
  else
    fail "Expected 1 fleet entry for deduped hostname, got $DEDUP_COUNT"
  fi
else
  fail "Skipping dedup check due to missing multi-use token"
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

# 10b. Scheduled jobs API + manual run
echo ""
echo "10b. Scheduled jobs create/run/history..."
JOB_CREATE=$(curl -sf -X POST "$CP_URL/api/v1/jobs" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"e2e scheduled\",\"command\":\"echo scheduled-ok\",\"schedule\":\"1h\",\"target\":{\"kind\":\"probe\",\"value\":\"$PROBE_ID\"},\"enabled\":true}")
JOB_ID=$(echo "$JOB_CREATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
if [[ -n "$JOB_ID" ]]; then
  pass "Job created (id=$JOB_ID)"
else
  fail "Job creation failed: $JOB_CREATE"
fi

JOB_RUN=$(curl -sf -X POST "$CP_URL/api/v1/jobs/$JOB_ID/run")
JOB_RUN_STATUS=$(echo "$JOB_RUN" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")
if [[ "$JOB_RUN_STATUS" == "dispatched" ]]; then
  pass "Job run dispatched"
else
  fail "Job run dispatch failed: $JOB_RUN"
fi

sleep 2
JOB_RUNS=$(curl -sf "$CP_URL/api/v1/jobs/$JOB_ID/runs")
JOB_RUN_COUNT=$(echo "$JOB_RUNS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('count',0))" 2>/dev/null || echo "0")
JOB_LAST_STATUS=$(echo "$JOB_RUNS" | python3 -c "import sys,json; runs=json.load(sys.stdin).get('runs', []); print(runs[0].get('status','') if runs else '')" 2>/dev/null || echo "")
if [[ "$JOB_RUN_COUNT" -ge 1 ]] && [[ "$JOB_LAST_STATUS" == "success" || "$JOB_LAST_STATUS" == "failed" ]]; then
  pass "Job run history recorded (count=$JOB_RUN_COUNT status=$JOB_LAST_STATUS)"
else
  fail "Job run history missing/invalid: $JOB_RUNS"
fi

JOB_RUNS_FILTERED=$(curl -sf "$CP_URL/api/v1/jobs/$JOB_ID/runs?status=failed&limit=1")
JOB_RUNS_FILTERED_COUNT=$(echo "$JOB_RUNS_FILTERED" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('count',0)); print(d.get('failed_count',-1))" 2>/dev/null || echo "0
-1")
JOB_FILTER_COUNT=$(echo "$JOB_RUNS_FILTERED_COUNT" | head -n1)
JOB_FILTER_FAILED=$(echo "$JOB_RUNS_FILTERED_COUNT" | tail -n1)
if [[ "$JOB_FILTER_COUNT" -ge 0 ]] && [[ "$JOB_FILTER_FAILED" -ge 0 ]]; then
  pass "Job run filters + failed summary available (count=$JOB_FILTER_COUNT failed=$JOB_FILTER_FAILED)"
else
  fail "Job run filter query invalid: $JOB_RUNS_FILTERED"
fi

JOB_FAIL_CREATE=$(curl -sf -X POST "$CP_URL/api/v1/jobs" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"e2e scheduled fail\",\"command\":\"false\",\"schedule\":\"1h\",\"target\":{\"kind\":\"probe\",\"value\":\"$PROBE_ID\"},\"enabled\":true}")
JOB_FAIL_ID=$(echo "$JOB_FAIL_CREATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
if [[ -n "$JOB_FAIL_ID" ]]; then
  pass "Failing job created (id=$JOB_FAIL_ID)"
else
  fail "Failing job creation failed: $JOB_FAIL_CREATE"
fi

JOB_FAIL_RUN=$(curl -sf -X POST "$CP_URL/api/v1/jobs/$JOB_FAIL_ID/run")
if echo "$JOB_FAIL_RUN" | grep -q "dispatched"; then
  pass "Failing job dispatched"
else
  fail "Failing job dispatch failed: $JOB_FAIL_RUN"
fi

sleep 2
FAILED_GLOBAL=$(curl -sf "$CP_URL/api/v1/jobs/runs?status=failed&limit=20")
FAILED_GLOBAL_COUNT=$(echo "$FAILED_GLOBAL" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('count',0)); print(d.get('failed_count',-1))" 2>/dev/null || echo "0
-1")
FAILED_GLOBAL_TOTAL=$(echo "$FAILED_GLOBAL_COUNT" | head -n1)
FAILED_GLOBAL_FAILED=$(echo "$FAILED_GLOBAL_COUNT" | tail -n1)
if [[ "$FAILED_GLOBAL_TOTAL" -ge 1 ]] && [[ "$FAILED_GLOBAL_FAILED" -ge 1 ]]; then
  pass "Global failed-run visibility works (count=$FAILED_GLOBAL_TOTAL failed=$FAILED_GLOBAL_FAILED)"
else
  fail "Global failed-run visibility missing: $FAILED_GLOBAL"
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
  -d '{"cidr":"127.0.0.0/24","scan_type":"ping","timeout_ms":50}')
if [[ "$DISCOVERY_SCAN_CODE" == "200" || "$DISCOVERY_SCAN_CODE" == "202" ]]; then
  pass "Discovery scan endpoint accepted request (status=$DISCOVERY_SCAN_CODE)"
else
  fail "Discovery scan endpoint returned $DISCOVERY_SCAN_CODE"
fi

# 43. Audit export JSONL
echo ""
echo "43. Audit export JSONL..."
AUDIT_JSONL_HEADERS=$(mktemp)
AUDIT_JSONL_BODY=$(mktemp)
curl -s -D "$AUDIT_JSONL_HEADERS" -o "$AUDIT_JSONL_BODY" "$CP_URL/api/v1/audit/export"
if grep -qi '^Content-Type: application/x-ndjson' "$AUDIT_JSONL_HEADERS" && [[ $(wc -c < "$AUDIT_JSONL_BODY") -gt 0 ]]; then
  pass "Audit JSONL export returns NDJSON and non-empty body"
else
  fail "Audit JSONL export invalid (headers=$(tr '\n' ' ' < "$AUDIT_JSONL_HEADERS"), bytes=$(wc -c < "$AUDIT_JSONL_BODY"))"
fi

# 44. Audit export CSV
echo ""
echo "44. Audit export CSV..."
AUDIT_CSV_HEADERS=$(mktemp)
AUDIT_CSV_BODY=$(mktemp)
curl -s -D "$AUDIT_CSV_HEADERS" -o "$AUDIT_CSV_BODY" "$CP_URL/api/v1/audit/export/csv"
CSV_HEADER=$(head -n 1 "$AUDIT_CSV_BODY" | tr -d '\r')
if grep -qi '^Content-Type: text/csv' "$AUDIT_CSV_HEADERS" && [[ "$CSV_HEADER" == "id,timestamp,type,probe_id,actor,summary" ]]; then
  pass "Audit CSV export returns CSV with expected headers"
else
  fail "Audit CSV export invalid (content-type=$(grep -i '^Content-Type:' "$AUDIT_CSV_HEADERS" | head -n1), header=$CSV_HEADER)"
fi

# 45. Audit purge endpoint
echo ""
echo "45. Audit purge endpoint..."
AUDIT_PURGE=$(curl -s -X DELETE "$CP_URL/api/v1/audit/purge?older_than=0s")
if echo "$AUDIT_PURGE" | python3 -c 'import sys,json; d=json.load(sys.stdin); assert "deleted" in d' 2>/dev/null; then
  pass "Audit purge endpoint returns deleted count"
else
  fail "Audit purge response invalid: $AUDIT_PURGE"
fi

# 46. MCP endpoint exists
echo ""
echo "46. MCP endpoint exists..."
MCP_HEADERS=$(mktemp)
MCP_BODY=$(mktemp)
MCP_CURL_EXIT=0
curl -s -D "$MCP_HEADERS" -o "$MCP_BODY" --max-time 2 "$CP_URL/mcp" || MCP_CURL_EXIT=$?
MCP_STATUS=$(awk 'toupper($1) ~ /^HTTP/ {code=$2} END {print code}' "$MCP_HEADERS")
if [[ "$MCP_STATUS" == "200" || "$MCP_STATUS" == "401" || "$MCP_STATUS" == "403" ]]; then
  pass "MCP endpoint reachable (status=$MCP_STATUS, curl_exit=$MCP_CURL_EXIT)"
else
  fail "MCP endpoint check failed (status=$MCP_STATUS, curl_exit=$MCP_CURL_EXIT, headers=$(tr '\n' ' ' < "$MCP_HEADERS"))"
fi

# 47. /version sanity after MCP wiring
echo ""
echo "47. Version endpoint still works..."
VERSION_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$CP_URL/version")
if [[ "$VERSION_STATUS" == "200" ]]; then
  pass "/version returns 200 after MCP wiring"
else
  fail "/version returned $VERSION_STATUS after MCP wiring"
fi

# 48. Network devices CRUD + probe endpoints (no real device required)
echo ""
echo "48. Network devices CRUD + probe endpoints..."
NETWORK_CREATE=$(curl -sf -X POST "$CP_URL/api/v1/network/devices" \
  -H "Content-Type: application/json" \
  -d '{"name":"e2e-router","host":"127.0.0.1","port":22,"vendor":"generic","username":"tester","auth_mode":"password","tags":["lab","e2e"]}')
NETWORK_ID=$(echo "$NETWORK_CREATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('device',{}).get('id',''))" 2>/dev/null || echo "")
if [[ -n "$NETWORK_ID" ]]; then
  pass "Network device created (id=$NETWORK_ID)"
else
  fail "Network device create failed: $NETWORK_CREATE"
fi

NETWORK_LIST=$(curl -sf "$CP_URL/api/v1/network/devices")
if echo "$NETWORK_LIST" | python3 -c "import sys,json; devices=json.load(sys.stdin).get('devices',[]); assert any(d.get('id')=='$NETWORK_ID' for d in devices)" 2>/dev/null; then
  pass "Network device visible in list"
else
  fail "Network device missing from list: $NETWORK_LIST"
fi

NETWORK_GET=$(curl -sf "$CP_URL/api/v1/network/devices/$NETWORK_ID")
if echo "$NETWORK_GET" | python3 -c "import sys,json; d=json.load(sys.stdin).get('device',{}); assert d.get('id')=='$NETWORK_ID' and d.get('name')=='e2e-router'" 2>/dev/null; then
  pass "Network device get endpoint returns created device"
else
  fail "Network device get failed: $NETWORK_GET"
fi

NETWORK_UPDATE=$(curl -sf -X PUT "$CP_URL/api/v1/network/devices/$NETWORK_ID" \
  -H "Content-Type: application/json" \
  -d '{"name":"e2e-router-updated","tags":["lab","updated"]}')
if echo "$NETWORK_UPDATE" | python3 -c "import sys,json; d=json.load(sys.stdin).get('device',{}); assert d.get('name')=='e2e-router-updated'" 2>/dev/null; then
  pass "Network device update endpoint works"
else
  fail "Network device update failed: $NETWORK_UPDATE"
fi

NETWORK_TEST=$(curl -sf -X POST "$CP_URL/api/v1/network/devices/$NETWORK_ID/test" -H "Content-Type: application/json" -d '{}')
if echo "$NETWORK_TEST" | python3 -c "import sys,json; r=json.load(sys.stdin).get('result',{}); assert 'reachable' in r and 'ssh_ready' in r" 2>/dev/null; then
  pass "Network device test endpoint returns structured result"
else
  fail "Network device test endpoint failed: $NETWORK_TEST"
fi

NETWORK_INV_CODE=$(curl -s -o /tmp/network-inv.json -w "%{http_code}" -X POST "$CP_URL/api/v1/network/devices/$NETWORK_ID/inventory" \
  -H "Content-Type: application/json" \
  -d '{}')
if [[ "$NETWORK_INV_CODE" == "200" || "$NETWORK_INV_CODE" == "502" ]]; then
  pass "Network device inventory endpoint reachable (status=$NETWORK_INV_CODE)"
else
  fail "Network device inventory endpoint returned unexpected status: $NETWORK_INV_CODE"
fi

NETWORK_DELETE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$CP_URL/api/v1/network/devices/$NETWORK_ID")
if [[ "$NETWORK_DELETE_CODE" == "200" || "$NETWORK_DELETE_CODE" == "204" ]]; then
  pass "Network device deleted"
else
  fail "Network device delete returned $NETWORK_DELETE_CODE"
fi

# 49. Network-device auth/permission behavior (via focused permission unit test)
echo ""
echo "49. Network-device auth/permission behavior..."
if go test ./internal/controlplane/server -run TestPermissionsNetworkDeviceRoutes -count=1 > /tmp/network-perms-test.log 2>&1; then
  pass "Permission test passed for network-device routes"
else
  fail "Permission test failed: $(tail -n 20 /tmp/network-perms-test.log)"
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
