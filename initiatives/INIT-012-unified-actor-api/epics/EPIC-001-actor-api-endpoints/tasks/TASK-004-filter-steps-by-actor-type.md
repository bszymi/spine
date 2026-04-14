---
id: TASK-004
type: Task
title: "Filter step execution query by actor type"
status: Completed
work_type: implementation
created: 2026-04-14
epic: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
  - type: depends_on
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/tasks/TASK-002-step-execution-query.md
---

# TASK-004 — Filter step execution query by actor type

---

## Context

The step execution query (`GET /execution/steps?actor_id=X&status=waiting`) currently returns all waiting steps regardless of the requesting actor's type. An `automated_system` actor sees steps meant for `human` or `ai_agent`, tries to claim them, and gets rejected with a 409. This wastes runner poll cycles and creates noisy logs.

The query must filter steps by the actor's type against each step's `eligible_actor_types` so actors only see steps they can actually claim.

## Deliverable

### Query filtering

When processing `GET /execution/steps?actor_id=X&status=waiting`:

1. Look up the actor's type from the actor_id
2. Filter step executions where `eligible_actor_types` includes the actor's type
3. Return only the eligible steps

### Behavior

- An `automated_system` actor only sees steps with `eligible_actor_types` containing `automated_system` (e.g. `validate`, `commit`)
- A `human` actor only sees steps with `eligible_actor_types` containing `human`
- If `eligible_actor_types` is empty or null, the step is visible to all actor types (backward compatible)

## Acceptance Criteria

- Step execution query filters by actor type
- automated_system actors do not see human-only or ai_agent-only steps
- Steps with no eligible_actor_types restriction remain visible to all
- Existing actor_id filtering still works alongside type filtering
