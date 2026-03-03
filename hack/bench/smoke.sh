#!/usr/bin/env bash
set -euo pipefail

# CI-safe smoke benchmark pass (single-iteration benchmark probes).
go test -run '^$' -bench '^BenchmarkSQLiteWriteThroughputContention/writers_1$|^BenchmarkAsyncJobQueueProcessingRate/max_in_flight_1$' -benchtime=1x -count=1 ./internal/controlplane/jobs

go test -run '^$' -bench '^BenchmarkSSEFanoutLatency/subscribers_10$' -benchtime=1x -count=1 ./internal/controlplane/websocket
