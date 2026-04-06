---
id: TASK-002
type: Task
title: "Execution Query API"
status: Completed
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-06
completed: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/tasks/TASK-001-execution-projection-schema.md
---

# TASK-002 — Execution Query API

---

## Purpose

Expose query endpoints for operational task discovery backed by execution projections. These queries serve AI execution engines, human dashboards, and the management platform.

---

## Deliverable

1. Implement query service methods:
   - `ListReadyTasks(ctx, filter) ([]ExecutionProjection, error)` — tasks ready to start (not blocked, not assigned)
   - `ListBlockedTasks(ctx, filter) ([]ExecutionProjection, error)` — tasks waiting on dependencies
   - `ListAssignedTasks(ctx, actorID) ([]ExecutionProjection, error)` — tasks assigned to an actor
   - `ListEligibleTasks(ctx, actorID) ([]ExecutionProjection, error)` — tasks an actor could claim based on skills and actor type
   - `ListAllTasks(ctx, filter) ([]ExecutionProjection, error)` — all tasks with their current blocking status

2. Each `ExecutionProjection` response must include blocking details:
   - `blocked: bool` — whether the task has unresolved `blocked_by` links
   - `blocked_by: []string` — paths of blocking tasks that are not yet terminal
   - `resolved_blockers: []string` — paths of blocking tasks that are terminal
   These fields are computed via `engine.IsBlocked()` against the projection store.

3. Expose via gateway REST endpoints:
   - `GET /api/v1/execution/tasks/ready`
   - `GET /api/v1/execution/tasks/blocked`
   - `GET /api/v1/execution/tasks/assigned?actor_id=...`
   - `GET /api/v1/execution/tasks/eligible?actor_id=...`
   - `GET /api/v1/execution/tasks` — all tasks with blocking status

4. Support pagination and workspace scoping on all endpoints

5. Update documentation:
   - Update `/architecture/api-operations.md` to document all execution query endpoints
   - Update `/architecture/access-surface.md` to include execution query APIs in the access surface

---

## Acceptance Criteria

- Ready tasks query excludes blocked and already-assigned tasks
- Blocked tasks query includes blocker details (specific blocker paths and their statuses)
- All task query responses include `blocked`, `blocked_by`, and `resolved_blockers` fields
- Assigned tasks query returns only tasks for the specified actor
- Eligible tasks query filters by actor's skills and type
- `ListAllTasks` returns every task with its current blocking status for dashboard views
- All endpoints are workspace-scoped
- Pagination works correctly
- Integration tests cover filter combinations including blocked/unblocked transitions
- Architecture documentation is updated to reflect execution query endpoints
