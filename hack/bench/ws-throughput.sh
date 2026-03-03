#!/usr/bin/env bash
set -euo pipefail

SERVER_URL="${SERVER_URL:-http://127.0.0.1:8080}"
CONNECTIONS="${CONNECTIONS:-200}"
SETUP_WORKERS="${SETUP_WORKERS:-64}"
DURATION="${DURATION:-20s}"
MESSAGE_BYTES="${MESSAGE_BYTES:-256}"
DIAL_TIMEOUT="${DIAL_TIMEOUT:-8s}"
WRITE_TIMEOUT="${WRITE_TIMEOUT:-2s}"
ADMIN_API_KEY="${ADMIN_API_KEY:-}"
INSECURE="${INSECURE:-false}"

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  cat <<USAGE
Usage: $(basename "$0")

Environment variables:
  SERVER_URL       Control-plane base URL (default: http://127.0.0.1:8080)
  CONNECTIONS      Probe websocket connections participating in throughput test (default: 200)
  SETUP_WORKERS    Concurrent register+dial workers (default: 64)
  DURATION         Throughput measurement duration (default: 20s)
  MESSAGE_BYTES    Approximate payload size for each message (default: 256)
  DIAL_TIMEOUT     WS dial timeout per connection (default: 8s)
  WRITE_TIMEOUT    Per-message write timeout (default: 2s)
  ADMIN_API_KEY    Optional admin key for auth-protected /api/v1/tokens
  INSECURE         true/false, skip TLS verification for HTTPS/WSS targets
USAGE
  exit 0
fi

go run ./hack/bench/cmd/wsbench \
  --mode throughput \
  --server "${SERVER_URL}" \
  --connections "${CONNECTIONS}" \
  --workers "${SETUP_WORKERS}" \
  --duration "${DURATION}" \
  --message-bytes "${MESSAGE_BYTES}" \
  --dial-timeout "${DIAL_TIMEOUT}" \
  --write-timeout "${WRITE_TIMEOUT}" \
  --admin-api-key "${ADMIN_API_KEY}" \
  --insecure="${INSECURE}"
