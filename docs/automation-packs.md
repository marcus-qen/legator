# Automation Packs (Stage 3.8.1)

Automation packs are machine-readable workflow definitions for repeatable operations.

This stage introduces **definition storage + validation only** (no runtime execution yet).

## Schema

Top-level object:

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
      "parameters": {
        "command": "pg_dump --schema-only"
      }
    },
    {
      "id": "archive",
      "action": "upload_artifact",
      "parameters": {
        "bucket": "legator-backups"
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
- At least one expected outcome is required (workflow-level or step-level).
- `expected_outcomes[].step_id`, when provided, must reference an existing step ID.

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

## Compatibility

All Stage 3.8.1 route and payload additions are additive and backward-compatible.
