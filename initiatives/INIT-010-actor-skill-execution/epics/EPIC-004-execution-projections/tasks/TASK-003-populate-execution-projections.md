---
id: TASK-003
type: Task
title: "Wire execution projection population into task lifecycle"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-06
last_updated: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/tasks/TASK-001-execution-projection-schema.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/tasks/TASK-002-execution-query-api.md
---

# TASK-003 — Wire Execution Projection Population into Task Lifecycle

---

## Purpose

The `projection.execution_projections` table and query endpoints exist, but `UpsertExecutionProjection` is never called. The table stays empty, so all `/execution/tasks/*` endpoints always return empty results.

Found during Codex review (P1).

---

## Deliverable

1. **Projection sync**: When the projection service syncs task artifacts, also upsert execution projections:
   - During `FullRebuild`, create execution projections for all task artifacts
   - During incremental sync, update execution projections when task artifacts change

2. **Run lifecycle updates**: Update execution projections when run state changes:
   - `StartRun` → set run_id, workflow_step, assignment_status = "unassigned"
   - `ClaimStep` / `ActivateStep` → set assigned_actor_id, assignment_status = "assigned"
   - `CompleteRun` → update status, clear assignment
   - `ReleaseStep` → clear assignment, set back to "unassigned"

3. **Blocking updates**: When `CheckAndEmitBlockingTransition` detects a task becoming unblocked, update its execution projection's blocked status

4. **Rebuild support**: Execution projections must be rebuildable from current artifact + runtime state (disposable database principle)

5. Tests:
   - Test projection is created/updated during run lifecycle
   - Test projection reflects blocking status changes
   - Test rebuild produces correct projections

---

## Acceptance Criteria

- `/execution/tasks` returns task data after artifacts are synced
- Execution projections update when runs start, steps are claimed/released, runs complete
- Blocking status in projections matches actual blocked_by link resolution
- Projections can be fully rebuilt from scratch
