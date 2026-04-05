---
id: TASK-002
type: Task
title: "Execution Query API"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
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
   - `ListReadyTasks(ctx, filter) ([]ExecutionProjection, error)` — tasks ready to start
   - `ListBlockedTasks(ctx, filter) ([]ExecutionProjection, error)` — tasks waiting on dependencies
   - `ListAssignedTasks(ctx, actorID) ([]ExecutionProjection, error)` — tasks assigned to an actor
   - `ListEligibleTasks(ctx, actorID) ([]ExecutionProjection, error)` — tasks an actor could claim based on skills and actor type

2. Expose via gateway REST endpoints:
   - `GET /api/v1/execution/tasks/ready`
   - `GET /api/v1/execution/tasks/blocked`
   - `GET /api/v1/execution/tasks/assigned?actor_id=...`
   - `GET /api/v1/execution/tasks/eligible?actor_id=...`

3. Support pagination and workspace scoping on all endpoints

4. Update documentation:
   - Update `/architecture/api-operations.md` to document all four execution query endpoints
   - Update `/architecture/access-surface.md` to include execution query APIs in the access surface

---

## Acceptance Criteria

- Ready tasks query excludes blocked and already-assigned tasks
- Blocked tasks query includes blocker details
- Assigned tasks query returns only tasks for the specified actor
- Eligible tasks query filters by actor's skills and type
- All endpoints are workspace-scoped
- Pagination works correctly
- Integration tests cover each query with realistic data
- Architecture documentation is updated to reflect execution query endpoints
