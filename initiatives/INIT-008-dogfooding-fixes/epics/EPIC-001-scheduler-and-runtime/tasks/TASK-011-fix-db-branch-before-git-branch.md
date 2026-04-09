---
id: TASK-011
type: Task
title: "Fix DB branch record created before Git branch with no rollback"
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

# TASK-011 — Fix DB Branch Record Created Before Git Branch with No Rollback

---

## Purpose

`createBranchRecord` in `/internal/divergence/service.go` (lines 162-183) creates the DB branch record first, then the Git branch. If Git branch creation fails, the DB record remains in `BranchStatusPending` with no corresponding Git branch. Subsequent convergence logic will try to merge a Git branch that doesn't exist.

---

## Deliverable

Reverse the order (create Git branch first, then DB row) or delete the DB row on Git failure.

---

## Acceptance Criteria

- No orphaned DB branch records exist after a failed Git branch creation
- Existing divergence tests pass
