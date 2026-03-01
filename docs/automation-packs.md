# Automation Packs (Stage 3.8.4)

Automation packs are machine-readable workflow definitions for repeatable operations.

Stage 3.8.4 extends Stage 3.8.3 guarded execution with **audit timeline + replay artifacts**:

- execution from stored definitions
- ordered step lifecycle/status tracking
- policy + approval guardrails before mutating steps
- per-step timeout and bounded retry controls
- optional rollback callbacks/hooks executed in reverse order on failure
- deterministic execution timeline events (event IDs + timestamps)
- persisted approval checkpoints/decisions and replay/debug artifacts

## Schema

Top-level definition object:

```json
{
  "metadata": {
    "id": "ops.backup-db",
    "name": "Ops DB Backup",
    "version": "1.0.0",
    "description": "Run pre-checks and archive database backups"
  },
  "inputs": [
    {
      "name": "environment",
      "type": "string",
      "required": true,
      "default": "prod",
      "constraints": {
        "min_length": 3,
        "max_length": 16,
        "enum": ["prod", "staging"]
      }
    }
  ],
  "approval": {
    "required": true,
    "minimum_approvers": 1,
    "approver_roles": ["ops", "security"],
    "policy": "high-risk-change"
  },
  "steps": [
    {
      "id": "prepare",
      "name": "Prepare backup",
      "action": "run_command",
      "mutating": false,
      "timeout_seconds": 30,
      "max_retries": 1,
      "parameters": {
        "command": "pg_dump --schema-only"
      }
    },
    {
      "id": "archive",
      "action": "upload_artifact",
      "timeout_seconds": 60,
      "max_retries": 2,
      "parameters": {
        "bucket": "legator-backups"
      },
      "rollback": {
        "action": "delete_artifact",
        "parameters": {
          "bucket": "legator-backups"
        },
        "timeout_seconds": 30
      },
      "approval": {
        "required": true,
        "minimum_approvers": 1,
        "approver_roles": ["ops"]
      },
      "expected_outcomes": [
        {
          "description": "Archive uploaded",
          "success_criteria": "artifact_uri is present",
          "required": true
        }
      ]
    }
  ],
  "expected_outcomes": [
    {
      "description": "Workflow completed successfully",
      "success_criteria": "all required outcomes are satisfied",
      "step_id": "archive",
      "required": true
    }
  ]
}
```

## Validation Rules (Server-Side)

Definition/schema validation:

- `metadata.id`, `metadata.name`, `metadata.version` are required.
- `metadata.id` must match `^[a-z0-9][a-z0-9._-]{1,127}$`.
- `metadata.version` must be semantic version format (`x.y.z`, optional suffix).
- At least one step is required.
- Step IDs must be unique and each step must include an `action`.
- Input names must be unique.
- Input type must be one of: `string`, `number`, `integer`, `boolean`, `array`, `object`.
- Input constraints are type-checked (for example, `min_length` only applies to `string`).
- Input `default` values and enum values must match the declared input type.
- Approval constraints enforce non-negative `minimum_approvers` and valid role-count bounds.
- Step execution controls enforce non-negative `timeout_seconds` and `max_retries` values.
- Rollback hooks require `rollback.action` when provided, and `rollback.timeout_seconds` cannot be negative.
- At least one expected outcome is required (workflow-level or step-level).
- `expected_outcomes[].step_id`, when provided, must reference an existing step ID.

Dry-run input validation:

- Required inputs must be supplied or have defaults.
- Provided input values must match declared types and constraints.
- Enum constraints are enforced on runtime input values.
- Unknown input keys are rejected.

## API

### Create definition

`POST /api/v1/automation-packs`

Response:

- `201 Created` with `{ "automation_pack": { ...definition... } }`
- `400 invalid_schema` when validation fails
- `409 conflict` when `(metadata.id, metadata.version)` already exists

### List definitions

`GET /api/v1/automation-packs`

Response:

```json
{
  "automation_packs": [
    {
      "metadata": {
        "id": "ops.backup-db",
        "name": "Ops DB Backup",
        "version": "1.0.0",
        "description": "Run pre-checks and archive database backups"
      },
      "input_count": 1,
      "step_count": 2,
      "created_at": "2026-03-01T13:00:00Z",
      "updated_at": "2026-03-01T13:00:00Z"
    }
  ]
}
```

### Get definition

`GET /api/v1/automation-packs/{id}`

Optional query parameter:

- `version=x.y.z` (if omitted, latest version is returned)

Response:

- `200 OK` with `{ "automation_pack": { ...definition... } }`
- `404 not_found` when no definition exists

### Dry-run definition + inputs (non-mutating)

`POST /api/v1/automation-packs/dry-run`

Request body:

```json
{
  "definition": { "metadata": { "id": "ops.backup-db", "name": "Ops DB Backup", "version": "1.0.0" }, "steps": [ ... ], "expected_outcomes": [ ... ] },
  "inputs": {
    "environment": "prod"
  }
}
```

Response (`200 OK`):

```json
{
  "dry_run": {
    "non_mutating": true,
    "metadata": { "id": "ops.backup-db", "name": "Ops DB Backup", "version": "1.0.0" },
    "resolved_inputs": { "environment": "prod" },
    "steps": [
      {
        "order": 1,
        "id": "prepare",
        "action": "run_command",
        "resolved_parameters": { "command": "journalctl -u app --since prod" },
        "predicted_risk": "medium",
        "approval_required": false,
        "policy_simulation": {
          "outcome": "allow",
          "risk_level": "medium",
          "summary": "command allowed by policy",
          "rationale": { "policy": "capacity-policy-v1" }
        }
      }
    ],
    "workflow_policy_simulation": {
      "outcome": "queue",
      "summary": "workflow requires manual approval"
    },
    "risk_summary": {
      "allow_count": 1,
      "queue_count": 1,
      "deny_count": 0,
      "highest": "queue"
    }
  }
}
```

Errors:

- `400 invalid_request` for malformed JSON
- `400 invalid_schema` when the submitted definition is invalid
- `400 invalid_inputs` when runtime inputs fail declared contracts

### Start execution from stored definition

`POST /api/v1/automation-packs/{id}/executions`

Request body (all fields optional except path id):

```json
{
  "version": "1.0.0",
  "inputs": {
    "environment": "prod"
  },
  "approval_context": {
    "workflow": {
      "approved": true,
      "approver_count": 1,
      "approved_by": ["operator@example.com"]
    },
    "steps": {
      "archive": {
        "approved": true,
        "approver_count": 1,
        "approved_by": ["ops@example.com"]
      }
    }
  }
}
```

Response (`201 Created`):

```json
{
  "execution": {
    "id": "apexec-1740843291984000000-1",
    "metadata": { "id": "ops.backup-db", "name": "Ops DB Backup", "version": "1.0.0" },
    "status": "succeeded",
    "started_at": "2026-03-01T13:34:51Z",
    "finished_at": "2026-03-01T13:34:53Z",
    "resolved_inputs": { "environment": "prod" },
    "steps": [
      {
        "order": 1,
        "id": "prepare",
        "action": "run_command",
        "mutating": false,
        "status": "succeeded",
        "attempts": 1,
        "timeout_seconds": 30,
        "max_retries": 1
      }
    ],
    "rollback_status": "not_required"
  }
}
```

Execution status values:

- workflow: `pending`, `running`, `succeeded`, `failed`, `blocked`
- step: `pending`, `running`, `succeeded`, `failed`, `timed_out`, `blocked`, `skipped`
- rollback status: `not_required`, `completed`, `partial`

Guardrail behaviour:

- Policy gate (`allow|queue|deny`) is enforced before mutating steps.
- Required workflow/step approvals are enforced before mutating steps.
- `queue`/`deny` policy outcomes block execution before step action dispatch.
- Step timeout + bounded retries are enforced using `timeout_seconds` and `max_retries`.
- Failed executions trigger rollback hooks (`rollback`) in reverse order for already-succeeded steps.

### Get execution status

`GET /api/v1/automation-packs/executions/{executionID}`

Response:

- `200 OK` with `{ "execution": { ... } }`
- `404 not_found` when execution id is unknown

Execution payload now includes additive audit/replay fields:

- `timeline[]` (ordered lifecycle events)
- `artifacts[]` (policy snapshots, approval checkpoints, stdout/stderr snippets, error context)

### Get execution timeline (deterministic replay order)

`GET /api/v1/automation-packs/executions/{executionID}/timeline`

Optional query params:

- `step_id=<stepID>` (filter events for one step)
- `type=<eventType>` (filter by event type)

Response (`200 OK`):

```json
{
  "execution_id": "apexec-1740843291984000000-1",
  "timeline": [
    {
      "id": "apexec-1740843291984000000-1-evt-000001",
      "sequence": 1,
      "timestamp": "2026-03-01T13:34:51Z",
      "type": "execution.started",
      "status": "running"
    }
  ],
  "replay": {
    "execution_id": "apexec-1740843291984000000-1",
    "deterministic_order": true,
    "event_count": 12,
    "artifact_count": 5,
    "ordered_event_ids": [
      "apexec-1740843291984000000-1-evt-000001"
    ],
    "first_timestamp": "2026-03-01T13:34:51Z",
    "last_timestamp": "2026-03-01T13:34:53Z"
  }
}
```

### Get execution artifacts

`GET /api/v1/automation-packs/executions/{executionID}/artifacts`

Optional query params:

- `step_id=<stepID>` (filter artifacts for one step)
- `type=<artifactType>` (for example `stdout_snippet`, `error_context`, `policy_rationale`)

Response (`200 OK`):

```json
{
  "execution_id": "apexec-1740843291984000000-1",
  "artifacts": [
    {
      "id": "apexec-1740843291984000000-1-art-000001",
      "event_id": "apexec-1740843291984000000-1-evt-000004",
      "step_id": "prepare",
      "attempt": 1,
      "type": "stdout_snippet",
      "timestamp": "2026-03-01T13:34:52Z",
      "data": {
        "snippet": "..."
      }
    }
  ]
}
```

Errors for both timeline/artifact routes:

- `400 invalid_request` when `executionID` is missing
- `404 not_found` when execution id is unknown

## Dry-run and Policy Simulation Guarantees

- Dry-run never executes commands, dispatches jobs, or mutates automation-pack storage.
- Policy simulation reuses existing command policy evaluation in simulation mode.
- Step/workflow predictions expose additive `allow|queue|deny` outcomes with rationale.
- Approval requirements in definition schema are reflected in dry-run queue predictions.

## Compatibility

All Stage 3.8.4 additions are additive and backward-compatible.
