#!/usr/bin/env bash
set -euo pipefail

# v0.10 M1.11 test-spin harness
#
# Scope:
# - launch one read-only mission
# - verify connectivity lane contract is populated for that run
# - create one approval-requiring request and complete approve flow
#
# Usage:
#   API_URL=http://127.0.0.1:8090 OP_TOKEN=... ./hack/v0.10-m1.11-cockpit-test-spin.sh
#
# Optional:
#   AGENT=watchman-light
#   OUT=/tmp/legator-m1.11-test-spin.json
#   KUBECTL=kubectl
#   APPROVAL_NAMESPACE=agents

API_URL="${API_URL:-}"
OP_TOKEN="${OP_TOKEN:-}"
AGENT="${AGENT:-watchman-light}"
OUT="${OUT:-/tmp/legator-m1.11-test-spin.json}"
KUBECTL_BIN="${KUBECTL:-kubectl}"
APPROVAL_NAMESPACE="${APPROVAL_NAMESPACE:-agents}"

if [[ -z "$API_URL" || -z "$OP_TOKEN" ]]; then
  echo "API_URL and OP_TOKEN are required" >&2
  exit 2
fi

TMP_DIR=$(mktemp -d)
cleanup(){ chmod -R u+w "$TMP_DIR" 2>/dev/null || true; rm -rf "$TMP_DIR" 2>/dev/null || true; }
trap cleanup EXIT

api_code(){
  local method="$1" path="$2" out="$3" body="${4:-}"
  if [[ -n "$body" ]]; then
    curl --max-time 20 -sS -o "$out" -w "%{http_code}" \
      -X "$method" "$API_URL$path" \
      -H "Authorization: Bearer $OP_TOKEN" \
      -H 'Content-Type: application/json' \
      -d "$body"
  else
    curl --max-time 20 -sS -o "$out" -w "%{http_code}" \
      -X "$method" "$API_URL$path" \
      -H "Authorization: Bearer $OP_TOKEN"
  fi
}

now_utc(){
  date -u +%Y-%m-%dT%H:%M:%SZ
}

START_TS="$(now_utc)"
TASK="M1.11 cockpit test-spin read-only mission $(date -u +%Y%m%dT%H%M%SZ)"

SC_RUN_TRIGGER=$(api_code POST "/api/v1/agents/${AGENT}/run" "$TMP_DIR/run-trigger.json" "{\"task\":\"$TASK\",\"autonomy\":\"observe\",\"target\":\"headscale://inventory\"}")

RUN_ID=""
for _ in $(seq 1 20); do
  SC_RUNS=$(api_code GET "/api/v1/runs?agent=${AGENT}" "$TMP_DIR/runs.json")
  if [[ "$SC_RUNS" == "200" ]]; then
    RUN_ID=$(python3 - "$START_TS" "$TMP_DIR/runs.json" <<'PY'
import json, sys
from datetime import datetime, timezone
start = datetime.fromisoformat(sys.argv[1].replace('Z','+00:00'))
items = json.load(open(sys.argv[2])).get('runs', [])
for r in items:
    created = r.get('createdAt')
    if not created:
        continue
    dt = datetime.fromisoformat(created.replace('Z','+00:00'))
    if dt >= start:
        print(r.get('name',''))
        break
PY
)
    if [[ -n "$RUN_ID" ]]; then
      break
    fi
  fi
  sleep 2
done

CONNECTIVITY_FOUND="false"
CONNECTIVITY_STATUS=""
CONNECTIVITY_MODE=""
CONNECTIVITY_ROUTE=""
if [[ -n "$RUN_ID" ]]; then
  for _ in $(seq 1 20); do
    SC_CONN=$(api_code GET "/api/v1/cockpit/connectivity?limit=50" "$TMP_DIR/connectivity.json")
    if [[ "$SC_CONN" == "200" ]]; then
      read -r CONNECTIVITY_FOUND CONNECTIVITY_STATUS CONNECTIVITY_MODE CONNECTIVITY_ROUTE <<EOF
$(python3 - "$RUN_ID" "$TMP_DIR/connectivity.json" <<'PY'
import json, sys
run_id = sys.argv[1]
runs = json.load(open(sys.argv[2])).get('runs', [])
row = next((r for r in runs if r.get('run') == run_id), None)
if not row:
    print('false   ')
else:
    tunnel = row.get('tunnel', {}) or {}
    cred = row.get('credential', {}) or {}
    print('true', tunnel.get('status',''), cred.get('mode',''), tunnel.get('routeId',''))
PY
)
EOF
      if [[ "$CONNECTIVITY_FOUND" == "true" ]]; then
        break
      fi
    fi
    sleep 2
  done
fi

SC_RUN_DETAIL="000"
TIMELINE_HAS_GATE="false"
if [[ -n "$RUN_ID" ]]; then
  for _ in $(seq 1 30); do
    SC_RUN_DETAIL=$(api_code GET "/api/v1/runs/${RUN_ID}" "$TMP_DIR/run-detail.json")
    if [[ "$SC_RUN_DETAIL" == "200" ]]; then
      read -r ACTION_COUNT RUN_PHASE <<EOF
$(python3 - "$TMP_DIR/run-detail.json" <<'PY'
import json, sys
run = json.load(open(sys.argv[1]))
status = run.get('status') or {}
actions = status.get('actions') or []
print(len(actions), status.get('phase',''))
PY
)
EOF
      if [[ "${ACTION_COUNT:-0}" -gt 0 || "$RUN_PHASE" == "Succeeded" || "$RUN_PHASE" == "Failed" || "$RUN_PHASE" == "Blocked" || "$RUN_PHASE" == "Escalated" ]]; then
        break
      fi
    fi
    sleep 2
  done
  if [[ "$SC_RUN_DETAIL" == "200" ]]; then
    TIMELINE_HAS_GATE=$(python3 - "$TMP_DIR/run-detail.json" <<'PY'
import json, sys
run = json.load(open(sys.argv[1]))
actions = ((run.get('status') or {}).get('actions') or [])
ok = len(actions) > 0 and any(bool((a.get('status') or '').strip()) for a in actions)
print('true' if ok else 'false')
PY
)
  fi
fi

APPROVAL_ID="m1-11-$(date -u +%H%M%S)-$RANDOM"
cat > "$TMP_DIR/approval.yaml" <<YAML
apiVersion: legator.io/v1alpha1
kind: ApprovalRequest
metadata:
  name: ${APPROVAL_ID}
  namespace: ${APPROVAL_NAMESPACE}
spec:
  agentName: ${AGENT}
  runName: ${RUN_ID:-manual-test}
  timeout: 30m
  context: "M1.11 synthetic approval-path probe"
  action:
    tool: kubectl.apply
    target: deployment/backstage -n backstage
    tier: service-mutation
    description: "Scale deployment for smoke test"
    args:
      replicas: "2"
YAML

$KUBECTL_BIN apply -f "$TMP_DIR/approval.yaml" >/dev/null

SC_APPROVALS_PENDING=$(api_code GET "/api/v1/approvals" "$TMP_DIR/approvals-pending.json")
PENDING_VISIBLE=$(python3 - "$APPROVAL_ID" "$TMP_DIR/approvals-pending.json" <<'PY'
import json, sys
name = sys.argv[1]
items = json.load(open(sys.argv[2])).get('approvals', [])
hit = any((x.get('metadata') or {}).get('name') == name for x in items)
print('true' if hit else 'false')
PY
)

SC_APPROVE=$(api_code POST "/api/v1/approvals/${APPROVAL_ID}" "$TMP_DIR/approve.json" '{"decision":"approve","reason":"m1.11 test-spin"}')
SC_APPROVALS_FINAL=$(api_code GET "/api/v1/approvals" "$TMP_DIR/approvals-final.json")
APPROVED_VISIBLE=$(python3 - "$APPROVAL_ID" "$TMP_DIR/approvals-final.json" <<'PY'
import json, sys
name = sys.argv[1]
items = json.load(open(sys.argv[2])).get('approvals', [])
hit = any((x.get('metadata') or {}).get('name') == name and ((x.get('status') or {}).get('phase') in ('Approved','approved')) for x in items)
print('true' if hit else 'false')
PY
)

python3 - <<PY > "$OUT"
import json
result = {
  "startTs": "$START_TS",
  "api": {
    "run_trigger_status": "$SC_RUN_TRIGGER",
    "runs_status": "${SC_RUNS:-000}",
    "run_detail_status": "$SC_RUN_DETAIL",
    "connectivity_status": "${SC_CONN:-000}",
    "approvals_pending_status": "$SC_APPROVALS_PENDING",
    "approve_status": "$SC_APPROVE",
    "approvals_final_status": "$SC_APPROVALS_FINAL",
  },
  "run": {
    "agent": "$AGENT",
    "id": "$RUN_ID",
    "task": "$TASK",
    "timeline_has_gate_data": "$TIMELINE_HAS_GATE" == "true",
  },
  "connectivity": {
    "found": "$CONNECTIVITY_FOUND" == "true",
    "tunnel_status": "$CONNECTIVITY_STATUS",
    "credential_mode": "$CONNECTIVITY_MODE",
    "route_id": "$CONNECTIVITY_ROUTE",
  },
  "approval": {
    "id": "$APPROVAL_ID",
    "pending_visible": "$PENDING_VISIBLE" == "true",
    "approved_visible": "$APPROVED_VISIBLE" == "true",
  },
}
checks = {
  "run_triggered": result["api"]["run_trigger_status"] in ("200", "202"),
  "run_id_observed": bool(result["run"]["id"]),
  "connectivity_visible_for_run": result["connectivity"]["found"],
  "approval_queue_visible": result["approval"]["pending_visible"],
  "approval_decision_applied": result["api"]["approve_status"] == "200" and result["approval"]["approved_visible"],
}
result["checks"] = checks
result["pass"] = all(checks.values())
print(json.dumps(result, indent=2))
PY

echo "M1.11 test-spin output: $OUT"
cat "$OUT"
