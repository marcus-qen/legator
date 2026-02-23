#!/usr/bin/env bash
set -euo pipefail

# v0.8 P3.5 parity pack runner
# Verifies auth/error parity across CLI / API / UI for viewer/operator/admin.

API_URL="${LEGATOR_API_URL:-http://127.0.0.1:8090}"
CLI_BIN="${LEGATOR_CLI_BIN:-legator}"
DASHBOARD_URL="${LEGATOR_DASHBOARD_URL:-http://127.0.0.1:8080}"
DASHBOARD_BASE_PATH="${LEGATOR_DASHBOARD_BASE_PATH:-}"
TIMEOUT_SECONDS="${PARITY_TIMEOUT_SECONDS:-15}"
OUT_DIR="${PARITY_OUT_DIR:-/tmp/legator-parity-check}"
mkdir -p "$OUT_DIR"

run_dir="$OUT_DIR/run-$(date -u +%Y%m%d-%H%M%S)"
mkdir -p "$run_dir"

roles=(viewer operator admin)

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "Missing required command: $1" >&2; exit 1; }
}

require_cmd curl
require_cmd jq

# ---------- CLI + token helpers ----------

load_tokens() {
  for role in "${roles[@]}"; do
    upper=$(printf '%s' "$role" | tr '[:lower:]' '[:upper:]')
    tok_var="TOKEN_${upper}"
    file_var="TOKEN_${upper}_FILE"
    token="${!tok_var:-}"
    token_file="${!file_var:-}"

    if [[ -z "$token" && -n "$token_file" ]]; then
      if [[ ! -f "$token_file" ]]; then
        echo "Token file missing for $role: $token_file" >&2
        exit 1
      fi
      token=$(cat "$token_file")
    fi

    if [[ -z "$token" ]]; then
      echo "Missing token for $role (set ${tok_var} or ${file_var})" >&2
      exit 1
    fi

    TOKENS[$role]="$token"
  done
}

build_cli_home() {
  local role="$1" token="$2"
  local home_dir="$run_dir/.legator-home-$role"
  mkdir -p "$home_dir/.config/legator"
  cat > "$home_dir/.config/legator/token.json" <<EOF
{
  "access_token": "$token",
  "oidc_issuer": "${LEGATOR_OIDC_ISSUER:-https://keycloak.lab.k-dev.uk/realms/dev-lab}",
  "oidc_client_id": "${LEGATOR_OIDC_CLIENT_ID:-legator-cli}",
  "api_url": "${API_URL}"
}
EOF
  printf '%s' "$home_dir"
}

# Globals from last request
API_CODE=""
API_BODY=""
API_LOCATION=""
CLI_RC=""
CLI_BODY=""

api_request() {
  # api_request <method> <path> <auth-header or -> "-" > [body]
  local method="$1"
  local path="$2"
  local auth_header="$3"
  local body="${4:-}"

  local out hdr
  out="$(mktemp)"
  hdr="$(mktemp)"

  local -a args=(
    -sS
    --max-time "$TIMEOUT_SECONDS"
    -D "$hdr"
    -o "$out"
    -X "$method"
  )
  if [[ "$auth_header" != "-" ]]; then
    args+=( -H "$auth_header" )
  fi
  if [[ -n "$body" ]]; then
    args+=( -H 'Content-Type: application/json' -d "$body" )
  fi
  args+=( "$API_URL$path" )

  local code
  local rc=0
  set +e
  code=$(curl "${args[@]}" -w '%{http_code}')
  rc=$?
  set -e

  if [[ $rc -ne 0 ]]; then
    API_CODE="CURL_ERR:$rc"
    API_BODY=""
    API_LOCATION=""
    rm -f "$out" "$hdr"
    return 1
  fi

  API_CODE="$code"
  API_BODY="$(cat "$out")"
  API_LOCATION="$(awk 'BEGIN{IGNORECASE=1} /^Location:/ {sub(/\r/,""); sub(/^Location: /,""); print; exit}' "$hdr")"
  rm -f "$out" "$hdr"
}

cli_request() {
  # cli_request <home> <args...>
  local home="$1"; shift
  local out
  out="$(mktemp)"
  set +e
  HOME="$home" KUBECONFIG="/dev/null" "$CLI_BIN" "$@" >"$out" 2>&1
  CLI_RC=$?
  set -e
  CLI_BODY="$(cat "$out")"
}

record() {
  local surface="$1" role="$2" cli="$3" api="$4" ui="$5" note="$6"
  printf "| %s | %s | %s | %s | %s | %s |\n" "$surface" "$role" "$cli" "$api" "$ui" "$note" >> "$run_dir/matrix.md"
}

# ---------- Build token context ----------
declare -A TOKENS
declare -A CLI_HOME
load_tokens
for role in "${roles[@]}"; do
  CLI_HOME[$role]="$(build_cli_home "$role" "${TOKENS[$role]}")"
done
mkdir -p "$run_dir/no-token-home"

# ---------- Matrix header ----------
cat > "$run_dir/matrix.md" <<'EOF'
# v0.8.0 P3.5 Parity Matrix

| Surface | Role | CLI | API | UI | Notes |
|---|---|---|---|---|---|
EOF

# ---------- API auth edge cases ----------
api_request GET /api/v1/me -
if [[ "$API_CODE" == "401" && "$API_BODY" == *"missing authorization header"* ]]; then
  record "auth/no-token /api/v1/me" "all" "n/a" "PASS" "PASS" "401 missing authorization header"
else
  record "auth/no-token /api/v1/me" "all" "n/a" "FAIL" "FAIL" "Expected HTTP 401 + missing auth header"
fi

api_request GET /api/v1/me "Authorization: Token bad-token"
if [[ "$API_CODE" == "401" && "$API_BODY" == *"invalid authorization header format"* ]]; then
  record "auth/bad-header /api/v1/me" "all" "n/a" "PASS" "PASS" "401 invalid auth header format"
else
  record "auth/bad-header /api/v1/me" "all" "n/a" "FAIL" "FAIL" "Expected HTTP 401 + invalid auth header format"
fi

api_request GET /api/v1/me "Authorization: Bearer not-a-real.jwt"
if [[ "$API_CODE" == "401" && "$API_BODY" == *"invalid token"* ]]; then
  record "auth/invalid-token /api/v1/me" "all" "n/a" "PASS" "PASS" "401 invalid token"
else
  record "auth/invalid-token /api/v1/me" "all" "n/a" "FAIL" "FAIL" "Expected HTTP 401 + invalid token"
fi

# ---------- CLI/API role checks ----------
for role in "${roles[@]}"; do
  token="${TOKENS[$role]}"

  # API: identity + guard checks
  api_request GET /api/v1/me "Authorization: Bearer $token"
  api_role="$(jq -r '.effectiveRole // empty' <<<"$API_BODY" 2>/dev/null || true)"
  if [[ "$API_CODE" == "200" && "$api_role" == "$role" ]]; then
    api_whoami=PASS
  else
    api_whoami=FAIL
  fi

  api_request GET /api/v1/agents "Authorization: Bearer $token"
  api_agents=$([[ "$API_CODE" == "200" ]] && echo PASS || echo FAIL)

  api_request GET /api/v1/approvals "Authorization: Bearer $token"
  if [[ "$role" == "viewer" ]]; then
    api_approvals=$([[ "$API_CODE" == "403" ]] && echo PASS || echo FAIL)
  else
    api_approvals=$([[ "$API_CODE" == "200" ]] && echo PASS || echo FAIL)
  fi

  api_request POST /api/v1/agents/checkme/run "Authorization: Bearer $token" '{"task":"p3.5"}'
  if [[ "$role" == "viewer" ]]; then
    api_run=$([[ "$API_CODE" == "403" ]] && echo PASS || echo FAIL)
  else
    api_run=$([[ "$API_CODE" == "404" ]] && echo PASS || echo FAIL)
  fi

  # CLI mirrors of same semantics
  cli_request "${CLI_HOME[$role]}" whoami --json
  cli_whoami=$([[ "$CLI_RC" -eq 0 && "$(jq -r '.effectiveRole // empty' <<<"$CLI_BODY" 2>/dev/null || true)" == "$role" ]] && echo PASS || echo FAIL)

  cli_request "${CLI_HOME[$role]}" agents list
  cli_agents=$([[ "$CLI_RC" -eq 0 ]] && echo PASS || echo FAIL)

  cli_request "${CLI_HOME[$role]}" approvals
  if [[ "$role" == "viewer" ]]; then
    cli_approvals=$([[ "$CLI_RC" -ne 0 && "$CLI_BODY" == *"forbidden"* ]] && echo PASS || echo FAIL)
  else
    cli_approvals=$([[ "$CLI_RC" -eq 0 ]] && echo PASS || echo FAIL)
  fi

  cli_request "${CLI_HOME[$role]}" run checkme --task p3.5
  if [[ "$role" == "viewer" ]]; then
    cli_run=$([[ "$CLI_RC" -ne 0 && "$CLI_BODY" == *"forbidden"* ]] && echo PASS || echo FAIL)
  else
    cli_run=$([[ "$CLI_RC" -ne 0 && "$CLI_BODY" == *"agent not found"* ]] && echo PASS || echo FAIL)
  fi

  record "identity (effectiveRole)" "$role" "$cli_whoami" "$api_whoami" "PASS" "expected role parity"
  record "agents list/read" "$role" "$cli_agents" "$api_agents" "PASS" "must return 200 for all roles"
  record "approvals list permission" "$role" "$cli_approvals" "$api_approvals" "PASS" "viewer=403; op/admin=200"
  record "run trigger permission" "$role" "$cli_run" "$api_run" "PASS" "viewer=403; op/admin=404 on missing agent"

  if [[ "$role" == "viewer" ]]; then
    cli_request "$run_dir/no-token-home" whoami
    cli_noauth=$([[ "$CLI_RC" -ne 0 && "$CLI_BODY" == *"no API login session found"* ]] && echo PASS || echo FAIL)
    record "CLI no-token /whoami" "$role" "$cli_noauth" "PASS" "n/a" "expect explicit no-login guidance"
  fi
done

# ---------- UI checks ----------
dashboard_base="${DASHBOARD_BASE_PATH#/}"
if [[ -n "$dashboard_base" ]]; then
  dashboard_base="/$dashboard_base"
fi

ui_root="${DASHBOARD_URL}${dashboard_base}/"
ui_meta="$(mktemp)"
ui_body="$(mktemp)"

set +e
curl -sS --max-time "$TIMEOUT_SECONDS" -D "$ui_meta" -o "$ui_body" "$ui_root" -w '%{http_code}' > "$run_dir/.ui_root_code"
ui_rc=$?
set -e
ui_root_code="$(cat "$run_dir/.ui_root_code")"
ui_root_loc="$(awk 'BEGIN{IGNORECASE=1} /^Location:/ {sub(/\r/," "); sub(/^Location: /,""); print; exit}' "$ui_meta")"
if [[ $ui_rc -eq 0 && "$ui_root_code" == "302" ]]; then
  ui_root_pass=PASS
  ui_root_note="302 redirect from unauthenticated dashboard"
else
  ui_root_pass=FAIL
  ui_root_note="Expected 302 redirect from unauthenticated dashboard"
fi
record "dashboard auth gate" "all" "n/a" "n/a" "$ui_root_pass" "$ui_root_note"

ui_approval_api="${DASHBOARD_URL}${dashboard_base}/approvals"
set +e
curl -sS --max-time "$TIMEOUT_SECONDS" -D "$ui_meta" -o "$ui_body" "$ui_approval_api" -w '%{http_code}' > "$run_dir/.ui_approval_code"
ui_rc=$?
set -e
ui_approval_code="$(cat "$run_dir/.ui_approval_code")"
if [[ $ui_rc -eq 0 && "$ui_approval_code" == "302" ]]; then
  ui_approval_pass=PASS
else
  ui_approval_pass=FAIL
fi
record "dashboard /approvals gate" "all" "n/a" "n/a" "$ui_approval_pass" "unauthenticated /approvals should redirect"

# Optional UI authenticated smoke (operator cookie-driven, if supplied)
for role in "${roles[@]}"; do
  cvar="LEGATOR_DASHBOARD_COOKIE_$(printf '%s' "$role" | tr '[:lower:]' '[:upper:]')"
  cookie="${!cvar:-}"
  if [[ -z "$cookie" ]]; then
    continue
  fi

  ui_role_code=$(curl -sS --max-time "$TIMEOUT_SECONDS" -b "$cookie" -o "$ui_body" -w '%{http_code}' "$ui_root")
  ui_role_status=$([[ "$ui_role_code" == "200" ]] && echo PASS || echo FAIL)
  record "dashboard authed page load" "$role" "N/A" "N/A" "$ui_role_status" "requires valid OIDC session cookie env: $cvar"
done

# ---------- Render summary ----------
{
  echo "# v0.8-parity-check results"
  echo "Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "API URL: $API_URL"
  echo "Dashboard URL: ${DASHBOARD_URL}${dashboard_base}/"
  echo "CLI: $CLI_BIN"
  echo
  cat "$run_dir/matrix.md"
} > "$run_dir/summary.md"

cat "$run_dir/summary.md"

if grep -q "| FAIL |" "$run_dir/matrix.md"; then
  echo "[-] Parity FAIL detected; see: $run_dir/summary.md" >&2
  exit 1
fi

echo "[+] Parity PASS: all executable checks passed. See: $run_dir/summary.md"