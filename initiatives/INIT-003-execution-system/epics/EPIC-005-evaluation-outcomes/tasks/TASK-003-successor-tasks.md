---
id: TASK-003
type: Task
title: Successor Task Creation
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-005-evaluation-outcomes/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-005-evaluation-outcomes/epic.md
---

# TASK-003 — Successor Task Creation

## Purpose

When a task is rejected with follow-up required, automatically create a linked successor task that preserves traceability.

## Deliverable

- Successor task creation logic in artifact service
- Automatic linking: successor task has `follow_up_to` link to rejected task
- Rejected task gets `follow_up_from` link to successor
- Successor task inherits parent epic and initiative
- Successor task starts in `Draft` status

## Acceptance Criteria

- Rejecting a task with `rejected_with_followup` creates a successor task
- Successor task is linked bidirectionally to the rejected task
- Successor task is placed in the same epic directory
- Rejected task's status transitions to `Rejected` in Git
- Original task's historical meaning is preserved (not mutated)
