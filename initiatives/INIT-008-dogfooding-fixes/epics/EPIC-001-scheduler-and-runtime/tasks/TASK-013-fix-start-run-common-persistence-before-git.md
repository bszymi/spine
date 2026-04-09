---
id: TASK-013
type: Task
title: "Fix startRunCommon persistence-before-Git inconsistency"
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

# TASK-013 — Fix startRunCommon Persistence-Before-Git Inconsistency

---

## Purpose

`startRunCommon` in `/internal/engine/run.go` (lines 48-65) writes the Run to the database before calling `git.CreateBranch()`. If Git branch creation fails, an orphaned pending run remains in the DB and the scheduler's recovery logic can later activate it despite no branch existing. This is the same class of bug as the divergence branch creation issue (TASK-011).

---

## Deliverable

Reverse the order (create Git branch first, then persist the run) or add rollback logic to delete the run record on Git failure.

---

## Acceptance Criteria

- No orphaned run records exist after a failed Git branch creation
- Scheduler recovery does not activate runs with missing branches
- Existing run tests pass
