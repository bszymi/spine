---
id: TASK-001
type: Task
title: "Dependency and Blocking Detection"
status: Pending
epic: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
initiative: /initiatives/INIT-010-actor-skill-execution/initiative.md
work_type: implementation
created: 2026-04-05
last_updated: 2026-04-05
links:
  - type: parent
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-002-task-eligibility/epic.md
---

# TASK-001 — Dependency and Blocking Detection

---

## Purpose

Ensure tasks that depend on other tasks cannot be executed until blocking tasks are completed. The system must detect and expose blocked tasks explicitly rather than relying on implicit precondition failure.

---

## Deliverable

1. Implement dependency resolution logic:
   - Given a task, resolve its `blocked_by` links from artifact metadata (the existing governed link type for blocker relationships)
   - Check completion status of each blocking task
   - Determine blocked/ready status

2. Add blocked status to task/step execution state:
   - `IsBlocked(ctx, taskPath) (bool, []string, error)` — returns blocked status and list of blocking task paths

3. Expose blocking information in step execution records so projections can include it

4. Emit event when a task transitions from blocked to ready

5. Update documentation:
   - Update `/architecture/engine-state-machine.md` to document the blocked-to-ready transition triggered by dependency completion
   - Update `/architecture/domain-model.md` to describe blocking relationships and their resolution
   - Update `/architecture/event-schemas.md` to document the blocked-to-ready transition event

---

## Acceptance Criteria

- Tasks with incomplete dependencies are detected as blocked
- Blocked tasks list their specific blockers
- When a blocking task completes, dependent tasks are re-evaluated
- Blocking detection handles circular dependency gracefully (error, not infinite loop)
- Integration tests cover blocked, unblocked, and multi-dependency scenarios
- Architecture documentation is updated to reflect dependency blocking detection
