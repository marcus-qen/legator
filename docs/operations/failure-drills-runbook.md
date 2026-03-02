# Failure drills runbook (C4)

This runbook covers the Pattern-2 C4 failure drills for runner crash handling,
control-plane restart recovery, and teardown leak checks.

## When to run

- Before releasing control-plane changes touching runner/jobs lifecycle.
- After upgrading container runtime integration for sandbox runners.
- During incident retrospectives where async jobs were stranded.

## Drill command

```bash
make drills GO=go
```

Equivalent script (used by CI):

```bash
scripts/drills/run-failure-drills.sh
```

The drill suite runs deterministic tests only (`TestFailureDrill_*`) in:

- `internal/controlplane/runner`
- `internal/controlplane/server`

## What each drill validates

### 1) Runner crash during active job

`TestFailureDrill_RunnerCrashDuringActiveJobCleansUp`

Validates that a forced runner crash emits command-error + teardown lifecycle
signals and eventually leaves zero tracked runner artifacts.

**Pass expectation:**
- `BackendEventCommandError` observed
- `BackendEventTeardown` observed
- No tracked backend runner entry
- No orphaned runtime artifact handles

### 2) Control-plane restart with queued + running jobs

`TestFailureDrill_ControlPlaneRestartRecoversQueuedAndRunningJobs`

Validates restart recovery semantics in persisted async jobs:
- previously **running** jobs are marked `expired`
- previously **queued** jobs remain `queued`

**Pass expectation:**
- running job state becomes `expired`
- status reason contains `control plane restarted`
- queued job state remains `queued`

### 3) Teardown leak check

`TestFailureDrill_TeardownLeakCheckRemovesOrphanArtifacts`

Validates that teardown can clean orphaned runtime artifacts even when the
control-plane no longer tracks that runner in-memory.

**Pass expectation:**
- teardown succeeds
- post-check reports zero runtime artifacts and no tracked runner handle

## Failure interpretation + remediation

### Runner crash drill fails

Symptoms:
- missing teardown event, or
- orphan artifact count > 0.

Actions:
1. Inspect `internal/controlplane/runner/container_backend.go` monitor/teardown
   paths (`monitor`, `Stop`, `Teardown`).
2. Confirm runtime adapter returns container-missing errors in a form handled by
   `isContainerMissing`.
3. Re-run drills locally after fix:
   ```bash
   make drills GO=go
   ```

### Restart recovery drill fails

Symptoms:
- running job still `running` after restart, or
- queued job unexpectedly transitioned.

Actions:
1. Verify `initJobs()` still calls `asyncJobsManager.ExpireStale(...)` on boot.
2. Verify `jobs.Store.ExpireRunningAsyncJobs` and
   `ExpireWaitingApprovalAsyncJobs` migration/state logic.
3. Re-run targeted package tests:
   ```bash
   go test ./internal/controlplane/server ./internal/controlplane/jobs -count=1
   ```

### Teardown leak drill fails

Symptoms:
- runtime artifact remains after teardown.

Actions:
1. Ensure teardown fallback path removes by deterministic runner container name
   (`containerNameForRunner`).
2. Confirm runtime remove path uses force deletion for stale artifacts.
3. Re-run runner tests:
   ```bash
   go test ./internal/controlplane/runner -count=1
   ```

## CI integration

GitHub Actions runs this suite in the `Failure drills` job via:

```yaml
- name: Run failure drill suite
  run: make drills GO=go
```
