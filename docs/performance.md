# Performance Characterization Suite

This document defines how to run and record Legator performance benchmarks.

## Goals

Characterize:

1. Maximum concurrent probe WebSocket connections (target: 1000+)
2. Probe WebSocket message throughput (messages/sec)
3. SQLite write throughput under contention
4. Async job queue processing rate (jobs/sec)
5. SSE fanout latency (event dispatch to all subscribers)

## Benchmark Inventory

| Benchmark | Command | Primary metric |
|---|---|---|
| Max concurrent WS probe connections | `hack/bench/ws-connections.sh` | `max_concurrent_observed`, `connection_setup_rate` |
| WS message throughput | `hack/bench/ws-throughput.sh` | `messages_per_second`, `payload_throughput_mib_per_sec` |
| SQLite write throughput under contention | `hack/bench/sqlite-write-throughput.sh` | `writes/s` |
| Job queue processing rate | `hack/bench/job-queue-throughput.sh` | `jobs/s` |
| SSE fanout latency | `hack/bench/sse-fanout-latency.sh` | `p50_ms`, `p95_ms`, `p99_ms` |
| CI-safe smoke | `hack/bench/smoke.sh` | quick sanity (single-iteration benchmark checks) |

## Test Methodology

### 1) Environment hygiene

- Run benchmarks on an otherwise idle host when possible.
- Pin to a fixed control-plane build/commit.
- Capture kernel, CPU, memory, disk type, and Go version.
- For connection-level benchmarks, use a consistent network path (prefer localhost or same L2 segment).

### 2) Repetition

- Run each benchmark at least 3 times.
- Record median and spread (min/max or stddev).
- For tuning changes, compare before/after medians with identical parameters.

### 3) Suggested parameter profiles

#### WebSocket connections

```bash
TARGET_CONNECTIONS=1000 SETUP_WORKERS=64 HOLD_FOR=10s hack/bench/ws-connections.sh
# scale-up passes
TARGET_CONNECTIONS=1500 SETUP_WORKERS=96 HOLD_FOR=10s hack/bench/ws-connections.sh
TARGET_CONNECTIONS=2000 SETUP_WORKERS=128 HOLD_FOR=10s hack/bench/ws-connections.sh
```

#### WebSocket throughput

```bash
CONNECTIONS=200 DURATION=20s MESSAGE_BYTES=256 hack/bench/ws-throughput.sh
CONNECTIONS=400 DURATION=20s MESSAGE_BYTES=256 hack/bench/ws-throughput.sh
```

#### SQLite / queue / SSE microbenchmarks

```bash
BENCH_TIME=5s hack/bench/sqlite-write-throughput.sh
BENCH_TIME=5s hack/bench/job-queue-throughput.sh
BENCH_TIME=5s hack/bench/sse-fanout-latency.sh
```

#### CI-safe smoke

```bash
hack/bench/smoke.sh
```

## Hardware / Environment Record (fill for each run)

| Field | Value |
|---|---|
| Date (UTC) | |
| Git commit | |
| Hostname / cloud instance type | |
| vCPU | |
| RAM | |
| Disk type | |
| Kernel | |
| Go version | |
| Control-plane config highlights | |
| Network path (local/remote) | |

## Results Template

### A) Concurrent WS connection ceiling

| Run | Target | Connected | Failures | Setup sec | Conn/sec | Notes |
|---|---:|---:|---:|---:|---:|---|
| 1 | 1000 | | | | | |
| 2 | 1500 | | | | | |
| 3 | 2000 | | | | | |

### B) WS message throughput

| Run | Connections | Duration | Msg bytes | Messages sent | Msg/sec | MiB/sec | Writer errors |
|---|---:|---:|---:|---:|---:|---:|---:|
| 1 | 200 | 20s | 256 | | | | |
| 2 | 400 | 20s | 256 | | | | |

### C) SQLite write throughput (contention)

Capture benchmark lines for:

- `BenchmarkSQLiteWriteThroughputContention/writers_1`
- `BenchmarkSQLiteWriteThroughputContention/writers_4`
- `BenchmarkSQLiteWriteThroughputContention/writers_16`
- `BenchmarkSQLiteWriteThroughputContention/writers_64`

| Writers | ns/op | writes/s |
|---:|---:|---:|
| 1 | | |
| 4 | | |
| 16 | | |
| 64 | | |

### D) Async queue processing rate

Capture benchmark lines for:

- `BenchmarkAsyncJobQueueProcessingRate/max_in_flight_1`
- `BenchmarkAsyncJobQueueProcessingRate/max_in_flight_8`
- `BenchmarkAsyncJobQueueProcessingRate/max_in_flight_32`

| Max in-flight | ns/op | jobs/s |
|---:|---:|---:|
| 1 | | |
| 8 | | |
| 32 | | |

### E) SSE fanout latency

Capture benchmark lines for:

- `BenchmarkSSEFanoutLatency/subscribers_10`
- `BenchmarkSSEFanoutLatency/subscribers_100`
- `BenchmarkSSEFanoutLatency/subscribers_500`
- `BenchmarkSSEFanoutLatency/subscribers_1000`

| Subscribers | p50 ms | p95 ms | p99 ms |
|---:|---:|---:|---:|
| 10 | | | |
| 100 | | | |
| 500 | | | |
| 1000 | | | |

## Scaling Limits, Bottlenecks, and Mitigations

Use this checklist after each characterization pass:

1. **Connection ceiling reached because registration or handshake failed first**
   - Mitigate: increase setup worker pool gradually, inspect top failure buckets (`error_top` output), verify token/API key auth path.
2. **WS throughput plateaus before CPU saturation**
   - Mitigate: inspect lock contention in websocket hub write/read paths, profile JSON marshal/unmarshal overhead, evaluate payload compaction.
3. **SQLite writes flatten under higher writer counts**
   - Current store runs with a single DB connection (`SetMaxOpenConns(1)`), so write serialization is expected.
   - Mitigate: review batching strategies, async buffering, and schema/index impact before changing connection model.
4. **Queue jobs/sec stalls with higher `max_in_flight`**
   - Mitigate: profile transition queries and scheduler lock contention; tune fetch batch size vs in-flight limits.
5. **SSE p95/p99 latency grows super-linearly with subscribers**
   - Mitigate: evaluate per-subscriber channel sizing, drop policy, and fanout worker model.

## Interpreting Results

When documenting scaling limits, explicitly state:

- **Observed ceiling** (e.g., "stable at 1400 WS probes; failures begin >1600")
- **Failure mode** (timeouts, auth failures, write errors, queue stalls)
- **First mitigation candidate** to test next
- **Regression threshold** for CI/ops (what constitutes unacceptable drift)
