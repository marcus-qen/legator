#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

echo "== v0.9 Release Readiness Harness =="
echo "repo: $ROOT_DIR"
echo

echo "[1/4] Required release artifacts present"
required=(
  "docs/v0.9-release-checklist.md"
  "RELEASE-NOTES-v0.9.0.md"
  "CHANGELOG.md"
  "hack/p3.3-parity-suite.sh"
)
for f in "${required[@]}"; do
  if [[ ! -f "$f" ]]; then
    echo "  missing: $f" >&2
    exit 1
  fi
  echo "  ok: $f"
done

grep -q "v0.9.0-rc1" CHANGELOG.md || { echo "  missing changelog v0.9.0-rc1 section" >&2; exit 1; }
echo "  ok: changelog contains v0.9.0-rc1"
echo

echo "[2/4] Command/API/dashboard test bundles"
PATH=/tmp/go/bin:$PATH go test ./cmd/legator ./cmd/dashboard ./cmd ./internal/api ./internal/chatops ./internal/dashboard

echo
echo "[3/4] Safety + policy packages"
PATH=/tmp/go/bin:$PATH go test ./internal/approval ./internal/anomaly ./internal/ratelimit ./internal/api/rbac

echo
echo "[4/4] Candidate signoff marker"
grep -q "Candidate signoff" docs/v0.9-release-checklist.md || { echo "  signoff section missing" >&2; exit 1; }
echo "  ok: candidate signoff section present"

echo
echo "v0.9 release readiness harness completed successfully."
