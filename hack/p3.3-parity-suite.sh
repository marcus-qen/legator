#!/usr/bin/env bash
set -euo pipefail

# v0.9.0 P3.3 parity suite (CLI/API/UI/ChatOps)
#
# Usage:
#   OP_TOKEN=... VIEWER_TOKEN=... API_URL=http://127.0.0.1:8090 ./hack/p3.3-parity-suite.sh
#
# Optional:
#   OUT=parity.json

API_URL="${API_URL:-}"
OP_TOKEN="${OP_TOKEN:-}"
VIEWER_TOKEN="${VIEWER_TOKEN:-}"
OUT="${OUT:-/tmp/legator-p3.3-parity.json}"

if [[ -z "$API_URL" || -z "$OP_TOKEN" || -z "$VIEWER_TOKEN" ]]; then
  echo "API_URL, OP_TOKEN, and VIEWER_TOKEN are required" >&2
  exit 2
fi

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
TMP_DIR=$(mktemp -d)
cleanup(){ chmod -R u+w "$TMP_DIR" 2>/dev/null || true; rm -rf "$TMP_DIR" 2>/dev/null || true; }
trap cleanup EXIT

api_code(){
  local token="$1" method="$2" path="$3" out="$4" body="${5:-}"
  if [[ -n "$body" ]]; then
    curl --max-time 15 -sS -o "$out" -w "%{http_code}" \
      -X "$method" "$API_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' \
      -d "$body"
  else
    curl --max-time 15 -sS -o "$out" -w "%{http_code}" \
      -X "$method" "$API_URL$path" \
      -H "Authorization: Bearer $token"
  fi
}

# --- API checks ---
SC_ME=$(api_code "$OP_TOKEN" GET /api/v1/me "$TMP_DIR/me.json")
SC_APPROVALS=$(api_code "$OP_TOKEN" GET /api/v1/approvals "$TMP_DIR/approvals.json")
SC_VIEWER_APPROVE=$(api_code "$VIEWER_TOKEN" POST /api/v1/approvals/nonexistent "$TMP_DIR/viewer-approve.json" '{"decision":"approve","reason":"p3.3-parity"}')

# --- CLI parity check (viewer deny path should surface forbidden) ---
HOME_DIR="$TMP_DIR/home"
mkdir -p "$HOME_DIR/.config/legator"
python3 - <<PY > "$HOME_DIR/.config/legator/token.json"
import json, datetime
now = datetime.datetime.now(datetime.timezone.utc)
print(json.dumps({
  "access_token": """$VIEWER_TOKEN""",
  "token_type": "Bearer",
  "issued_at": now.isoformat().replace('+00:00','Z'),
  "expires_at": (now+datetime.timedelta(hours=1)).isoformat().replace('+00:00','Z'),
  "api_url": "$API_URL",
  "oidc_issuer": "parity-suite",
  "oidc_client_id": "parity-suite"
}))
PY

GO_MOD_CACHE="${GO_MOD_CACHE:-/tmp/go-mod-cache}"
GO_BUILD_CACHE="${GO_BUILD_CACHE:-/tmp/go-build-cache}"
mkdir -p "$GO_MOD_CACHE" "$GO_BUILD_CACHE"

set +e
(
  cd "$ROOT_DIR"
  HOME="$HOME_DIR" GOMODCACHE="$GO_MOD_CACHE" GOCACHE="$GO_BUILD_CACHE" /tmp/go/bin/go run ./cmd/legator approve nonexistent parity-suite
) >"$TMP_DIR/cli.out" 2>"$TMP_DIR/cli.err"
CLI_RC=$?
set -e

# --- ChatOps guard checks (unit tests) ---
set +e
(
  cd "$ROOT_DIR"
  PATH=/tmp/go/bin:$PATH GOMODCACHE="$GO_MOD_CACHE" GOCACHE="$GO_BUILD_CACHE" go test ./internal/chatops -run 'TestApproveStartsTypedConfirmationWithoutMutatingAPI|TestConfirmFlowHonorsForbiddenDecisionPath|TestConfirmFlowExpires|TestConfirmCodeMismatchThenSuccess'
) >"$TMP_DIR/chatops.out" 2>"$TMP_DIR/chatops.err"
CHATOPS_RC=$?
set -e

# --- UI guard checks (dashboard tests) ---
set +e
(
  cd "$ROOT_DIR"
  PATH=/tmp/go/bin:$PATH GOMODCACHE="$GO_MOD_CACHE" GOCACHE="$GO_BUILD_CACHE" go test ./internal/dashboard -run 'TestHandleApprovalActionRequiresAPIBridge|TestHandleApprovalActionForwardsToAPI|TestHandleApprovalActionPropagatesForbidden|TestMakeDashboardJWTIncludesUserClaims'
) >"$TMP_DIR/dashboard.out" 2>"$TMP_DIR/dashboard.err"
DASHBOARD_RC=$?
set -e

# --- UI parity signal (current dashboard mutation path implementation) ---
if grep -q "decideApprovalViaAPI(ctx" "$ROOT_DIR/internal/dashboard/server.go"; then
  UI_APPROVAL_PATH="api-forwarded"
elif grep -q "updateApproval(ctx" "$ROOT_DIR/internal/dashboard/server.go"; then
  UI_APPROVAL_PATH="direct-k8s-update"
else
  UI_APPROVAL_PATH="unknown"
fi

python3 - <<PY > "$OUT"
import json
from pathlib import Path

def txt(p):
  try:
    return Path(p).read_text().strip()
  except Exception:
    return ""

viewer_body = txt("$TMP_DIR/viewer-approve.json")
cli_err = txt("$TMP_DIR/cli.err")

checks = {
  "api_me_200": "$SC_ME" == "200",
  "api_approvals_200": "$SC_APPROVALS" == "200",
  "api_viewer_approve_403": "$SC_VIEWER_APPROVE" == "403",
  "cli_nonzero": $CLI_RC != 0,
  "cli_forbidden_message": "forbidden" in cli_err.lower(),
  "chatops_tests_pass": $CHATOPS_RC == 0,
  "dashboard_tests_pass": $DASHBOARD_RC == 0,
  "ui_path_api_forwarded": "$UI_APPROVAL_PATH" == "api-forwarded",
}

result = {
  "api": {
    "me_status": "$SC_ME",
    "approvals_status": "$SC_APPROVALS",
    "viewer_approve_status": "$SC_VIEWER_APPROVE",
    "viewer_approve_body": viewer_body,
  },
  "cli": {
    "viewer_approve_rc": $CLI_RC,
    "stderr": cli_err,
  },
  "chatops": {
    "test_rc": $CHATOPS_RC,
    "stdout": txt("$TMP_DIR/chatops.out"),
    "stderr": txt("$TMP_DIR/chatops.err"),
  },
  "dashboard": {
    "test_rc": $DASHBOARD_RC,
    "stdout": txt("$TMP_DIR/dashboard.out"),
    "stderr": txt("$TMP_DIR/dashboard.err"),
  },
  "ui": {
    "approval_action_path": "$UI_APPROVAL_PATH"
  },
  "checks": checks,
  "pass": all(checks.values()),
}
print(json.dumps(result, indent=2))
PY

echo "Parity output: $OUT"
cat "$OUT"
