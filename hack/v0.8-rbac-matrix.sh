#!/usr/bin/env bash
set -euo pipefail

# Manual-assisted RBAC matrix runner for v0.8.0.
# Requires: legator CLI, jq

API_URL="${LEGATOR_API_URL:-https://legator.lab.k-dev.uk}"
ISSUER="${LEGATOR_OIDC_ISSUER:-https://keycloak.lab.k-dev.uk/realms/dev-lab}"
CLIENT_ID="${LEGATOR_OIDC_CLIENT_ID:-legator-cli}"
OUT_DIR="${1:-./artifacts/rbac-matrix}"
mkdir -p "$OUT_DIR"

roles=(viewer operator admin)

for role in "${roles[@]}"; do
  echo ""
  echo "=== ROLE: $role ==="
  echo "Please authenticate as the $role user in the browser when prompted."

  legator logout || true
  legator login --issuer "$ISSUER" --client-id "$CLIENT_ID" --api-url "$API_URL"

  legator whoami --json | tee "$OUT_DIR/$role-whoami.json" >/dev/null

  echo "Identity summary:"
  jq -r '.email + " | role=" + .effectiveRole' "$OUT_DIR/$role-whoami.json"

  echo "Permission summary:"
  jq -r '.permissions | to_entries[] | "  " + .key + ": " + (if .value.allowed then "allow" else "deny" end)' "$OUT_DIR/$role-whoami.json" | sort

done

echo ""
echo "Saved artifacts in: $OUT_DIR"
