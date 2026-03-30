---
id: TASK-006
type: Task
title: Implement planning run cancellation cleanup
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
  - type: blocked_by
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/tasks/TASK-003-implement-start-planning-run.md
---

# TASK-006 — Implement Planning Run Cancellation Cleanup

---

## Purpose

Ensure that cancelling a planning run deletes the run branch so no artifacts leak onto main.

The existing `CancelRun()` in `internal/engine/run.go` transitions the run to `cancelled` but branch cleanup behavior for planning runs must be verified and extended if needed. Since planning run branches contain artifacts that were never approved, the branch must be deleted on cancellation.

---

## Deliverable

`internal/engine/run.go`

Verify and extend `CancelRun()`:
- After transitioning to `cancelled`, call `CleanupRunBranch()` to delete the run branch
- Ensure this works for planning runs (branch contains uncommitted-to-main artifacts)
- If `CleanupRunBranch()` already handles this, add a test to confirm

---

## Acceptance Criteria

- Cancelling a planning run deletes the run branch
- Artifacts created on the branch do not appear on main after cancellation
- Branch cleanup failure is logged but does not block the cancellation transition
- Unit test confirms branch deletion on planning run cancellation
