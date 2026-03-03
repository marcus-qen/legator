#!/usr/bin/env bash
set -euo pipefail

SERVER_URL="${SERVER_URL:-http://127.0.0.1:8080}"
TARGET_CONNECTIONS="${TARGET_CONNECTIONS:-1000}"
SETUP_WORKERS="${SETUP_WORKERS:-64}"
HOLD_FOR="${HOLD_FOR:-10s}"
DIAL_TIMEOUT="${DIAL_TIMEOUT:-8s}"
ADMIN_API_KEY="${ADMIN_API_KEY:-}"
INSECURE="${INSECURE:-false}"

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  cat <<USAGE
Usage: $(basename "$0")

Environment variables:
  SERVER_URL          Control-plane base URL (default: http://127.0.0.1:8080)
  TARGET_CONNECTIONS  Target concurrent probe websocket connections (default: 1000)
  SETUP_WORKERS       Concurrent register+dial workers (default: 64)
  HOLD_FOR            How long to hold connections open (default: 10s)
  DIAL_TIMEOUT        WS dial timeout per connection (default: 8s)
  ADMIN_API_KEY       Optional admin key for auth-protected /api/v1/tokens
  INSECURE            true/false, skip TLS verification for HTTPS/WSS targets
USAGE
  exit 0
fi

go run ./hack/bench/cmd/wsbench \
  --mode connections \
  --server "${SERVER_URL}" \
  --connections "${TARGET_CONNECTIONS}" \
  --workers "${SETUP_WORKERS}" \
  --duration "${HOLD_FOR}" \
  --dial-timeout "${DIAL_TIMEOUT}" \
  --admin-api-key "${ADMIN_API_KEY}" \
  --insecure="${INSECURE}"
