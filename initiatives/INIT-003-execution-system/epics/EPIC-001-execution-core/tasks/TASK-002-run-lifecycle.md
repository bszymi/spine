---
id: TASK-002
type: Task
title: Run Lifecycle Management
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-001-execution-core/epic.md
---

# TASK-002 — Run Lifecycle Management

## Purpose

Implement run lifecycle orchestration: creating runs from tasks, activating them, tracking state, and completing or failing them.

## Deliverable

- `internal/engine/run.go` — Run lifecycle methods on the orchestrator
- `StartRun(taskPath)` — resolve workflow, create run, persist, activate first step
- `CompleteRun(runID)` — transition to completed when terminal step reached
- `FailRun(runID, reason)` — transition to failed on permanent failure
- `CancelRun(runID)` — cancel an active run
- Integration with existing run state machine (`workflow.RunMachine`)

## Acceptance Criteria

- `StartRun` creates a run in `pending`, then transitions to `active`
- First step is activated after run activation
- `CompleteRun` transitions run to `completed` state
- `FailRun` transitions run to `failed` with error detail
- Invalid state transitions are rejected with appropriate errors
- All transitions are persisted to the store
