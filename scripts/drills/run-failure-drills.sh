#!/usr/bin/env bash
set -euo pipefail

GO_BIN="${GO_BIN:-${GO:-go}}"
export LEGATOR_DRILLS_DETERMINISTIC="${LEGATOR_DRILLS_DETERMINISTIC:-1}"

echo "[drills] deterministic mode=${LEGATOR_DRILLS_DETERMINISTIC}"
"${GO_BIN}" test \
  ./internal/controlplane/runner \
  ./internal/controlplane/server \
  -run ^TestFailureDrill_ \
  -count=1
