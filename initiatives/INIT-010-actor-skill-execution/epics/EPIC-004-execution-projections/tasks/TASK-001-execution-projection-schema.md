---
id: TASK-001
type: Task
title: "Execution Projection Schema"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-004-execution-projections/epic.md
---

# TASK-001 — Execution Projection Schema

---

## Purpose

Define and implement the projection schema used for operational execution queries. This combines artifact state (from Git) with execution state (from runtime store) into a single queryable view.

---

## Deliverable

1. Define `ExecutionProjection` schema with fields:
   - `TaskID`, `TaskPath`, `Title`, `Status` (from artifact)
   - `WorkflowID`, `WorkflowStep` (from run context)
   - `RequiredSkills` (from workflow step definition)
   - `AllowedActorTypes` (from execution mode)
   - `Dependencies` (from artifact links)
   - `BlockedStatus` (computed: blocked/ready)
   - `BlockedBy` (list of blocking task paths)
   - `AssignedActorID` (from assignment, nullable)
   - `AssignmentStatus` (unassigned/assigned/in_progress)
   - `WorkspaceID`
   - `LastUpdated`

2. Add database table or materialized view for execution projections

3. Implement projection update mechanism:
   - Update on workflow events (step assigned, completed, released)
   - Update on artifact events (status change, link change)
   - Update on dependency completion (blocked -> ready transition)

4. Ensure projections are rebuildable from current state (disposable database principle)

5. Update documentation:
   - Update `/architecture/data-model.md` to document the execution projection table schema
   - Update `/architecture/runtime-schema.md` to include the execution projection in the runtime database schema
   - Update `/architecture/components.md` to describe the execution projection service as a component

---

## Acceptance Criteria

- Execution projection combines artifact and execution state in a single queryable record
- Projections update automatically when relevant events fire
- Projections can be fully rebuilt from scratch
- Schema supports all filters needed by the Execution Query API (TASK-002)
- Integration tests verify projection accuracy after state changes
- Architecture documentation is updated to reflect the execution projection schema
