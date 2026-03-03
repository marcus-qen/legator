#!/usr/bin/env bash
set -euo pipefail

BENCH_TIME="${BENCH_TIME:-5s}"
COUNT="${COUNT:-1}"

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  cat <<USAGE
Usage: $(basename "$0")

Environment variables:
  BENCH_TIME  go test -benchtime value (default: 5s)
  COUNT       go test -count value (default: 1)
USAGE
  exit 0
fi

go test -run '^$' -bench '^BenchmarkAsyncJobQueueProcessingRate$' -benchmem -benchtime="${BENCH_TIME}" -count="${COUNT}" ./internal/controlplane/jobs
