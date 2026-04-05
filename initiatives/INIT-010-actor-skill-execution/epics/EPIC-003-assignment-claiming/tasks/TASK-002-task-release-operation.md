---
id: TASK-002
type: Task
title: "Task Release Operation"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/epic.md
  - type: depends_on
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-003-assignment-claiming/tasks/TASK-001-task-claim-operation.md
---

# TASK-002 — Task Release Operation

---

## Purpose

Allow actors to release a claimed or assigned task back into the pool. This enables reallocation to another actor when the current assignee cannot complete the work.

---

## Deliverable

1. Implement `ReleaseTask(ctx, actorID, assignmentID, reason string) error`:
   - Validate the actor is the current assignee
   - Cancel the assignment
   - Transition step execution back to `waiting` state
   - Add releasing actor to excluded actors list for next assignment attempt (prevent immediate re-claim)
   - Emit `EventTaskReleased` (new event type) with actor, task, reason, timestamp

2. Define `EventTaskReleased` event type in domain events

3. Expose via gateway REST endpoint:
   - `POST /api/v1/execution/release` with body `{ "actor_id": "...", "assignment_id": "...", "reason": "..." }`

4. Update documentation:
   - Update `/architecture/engine-state-machine.md` to document the release transition (assigned -> waiting via actor-initiated release)
   - Update `/architecture/api-operations.md` to document the release endpoint
   - Update `/architecture/event-schemas.md` to document the new `EventTaskReleased` event type and its payload

---

## Acceptance Criteria

- Assigned actor can release a task
- Released task returns to claimable state
- Non-assignee cannot release another actor's task
- Released task excludes the releasing actor from immediate re-assignment
- Audit event includes release reason
- Task in terminal state (completed, failed) cannot be released
- Integration tests cover release, re-claim by different actor, and validation cases
- Architecture documentation is updated to reflect the release operation
