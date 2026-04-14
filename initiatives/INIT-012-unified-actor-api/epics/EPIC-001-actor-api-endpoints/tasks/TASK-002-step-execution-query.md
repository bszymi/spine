---
id: TASK-002
type: Task
title: "Add Step-Execution Query Endpoint"
status: Pending
work_type: implementation
created: 2026-04-14
epic: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
---

# TASK-002 — Add Step-Execution Query Endpoint

---

## Context

Automated actors (runners) need to discover steps assigned to them within active runs. The existing `/execution/candidates` endpoint returns tasks ready for new runs, not step executions within active runs.

When a run advances from `execute` to `validate`, the validate step exists as a step execution inside the run. The candidates API can't find it because the task is already In Progress.

## Deliverable

### Route

```go
r.Get("/execution/steps", s.handleListStepExecutions)
```

### Query parameters

| Param | Type | Description |
|-------|------|-------------|
| `actor_id` | string | Filter by eligible actor ID |
| `actor_type` | string | Filter by eligible actor type |
| `status` | string | Step execution status: waiting, assigned |
| `limit` | int | Max results (default 10) |

### Response

```json
{
  "steps": [
    {
      "execution_id": "run-abc-validate-1",
      "run_id": "run-abc",
      "step_id": "validate",
      "task_path": "initiatives/.../tasks/TASK-001.md",
      "status": "waiting",
      "attempt": 1,
      "created_at": "2026-04-14T10:00:00Z"
    }
  ]
}
```

### Implementation

Query step_executions for non-terminal steps where:
- The workflow step's `eligible_actor_types` includes the requested actor_type
- AND the step's status matches the requested status
- AND the parent run is active

Returns empty array (not error) when no steps are available.

## Acceptance Criteria

- Actors can query for steps assigned to their type
- Returns step executions within active runs
- Filters by status (waiting, assigned)
- Runner's spine_client.ListAssignedSteps() works against this endpoint
