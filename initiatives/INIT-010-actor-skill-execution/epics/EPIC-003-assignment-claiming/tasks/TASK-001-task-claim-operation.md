---
id: TASK-001
type: Task
title: "Task Claim Operation"
status: Completed
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-06
completed: 2026-04-06
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/epic.md
---

# TASK-001 — Task Claim Operation

---

## Purpose

Implement the operation allowing an actor to claim a task or workflow step from the execution pool. This enables a pull-based model where actors (AI agents, humans via dashboard) actively select work.

---

## Deliverable

1. Implement `ClaimStep(ctx, actorID, executionID) (Assignment, error)`:
   - `executionID` identifies a specific step execution (run + step), not a task path — a task can have multiple runs and each run has distinct step executions
   - Validate step execution is in claimable state (waiting, not blocked, not already assigned)
   - Validate actor eligibility (skill match, actor type compatibility)
   - Atomically create assignment (optimistic locking to prevent double-claim)
   - Transition step execution to `assigned` state
   - Emit `EventStepAssigned` with claim context

2. Handle contention:
   - If two actors claim simultaneously, one succeeds, the other gets a conflict error
   - Use database-level optimistic locking or row-level lock

3. Expose via gateway REST endpoint:
   - `POST /api/v1/execution/claim` with body `{ "actor_id": "...", "execution_id": "..." }`

4. Update documentation:
   - Update `/architecture/engine-state-machine.md` to document the claim transition (waiting -> assigned via actor-initiated claim)
   - Update `/architecture/api-operations.md` to document the claim endpoint
   - Update `/architecture/actor-model.md` to describe the pull-based claim model alongside the existing push-based assignment
   - Update `/architecture/event-schemas.md` to document claim context in `EventStepAssigned`

---

## Acceptance Criteria

- Actor can claim a ready, unassigned task
- Claim fails with clear error if task is already assigned
- Claim fails if actor lacks required skills
- Claim fails if actor type is not allowed by execution mode
- Concurrent claims are handled safely (no double assignment)
- Audit event is emitted for every claim
- Integration tests cover success, conflict, and validation failure cases
- Architecture documentation is updated to reflect the claim operation
