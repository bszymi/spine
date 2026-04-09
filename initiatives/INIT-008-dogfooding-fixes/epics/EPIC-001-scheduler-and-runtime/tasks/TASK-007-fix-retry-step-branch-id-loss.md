---
id: TASK-007
type: Task
title: "Fix RetryStep losing BranchID on branch step retries"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: bugfix
created: 2026-04-09
last_updated: 2026-04-09
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-007 — Fix RetryStep Losing BranchID on Branch Step Retries

---

## Purpose

`RetryStep` in `/internal/engine/retry.go` (lines 78-86) creates retry executions without copying `exec.BranchID`. The new execution has `BranchID: ""`. When the retried step completes, `SubmitStepResult` treats it as a top-level step, bypassing all branch state management. A transient failure on a branch step corrupts the run state machine.

---

## Deliverable

Copy `exec.BranchID` into `nextExec` in the `RetryStep` function.

---

## Acceptance Criteria

- Retried branch steps preserve BranchID in the new execution
- SubmitStepResult correctly routes retried branch steps through `completeBranchStep`
- Existing tests pass; add a test for branch step retry
