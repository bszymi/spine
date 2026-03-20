---
id: TASK-003
type: Task
title: Run State Machine
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
---

# TASK-003 — Run State Machine

## Purpose

Implement the Run state machine with all transitions, guards, and effects from Engine State Machine §2.

## Deliverable

- Run state machine: pending → active → paused/committing/completed/failed/cancelled
- Transition evaluation with triggers and guards
- Effect execution (create StepExecution, emit events, trigger Git commits)
- Invalid transition rejection
- `committing` state for Git durable boundary
- Task branch creation at Run start (per Git Integration §6.1)

## Acceptance Criteria

- Every valid transition from Engine State Machine §2.2 works correctly
- Every invalid transition from §2.3 is rejected
- `committing` state correctly bridges runtime and Git
- Git commit success transitions to `completed`; failure to `failed`
- State changes are persisted before effects are executed
- Unit tests verify every transition in the matrix
- Integration tests verify Git commit boundary behavior
