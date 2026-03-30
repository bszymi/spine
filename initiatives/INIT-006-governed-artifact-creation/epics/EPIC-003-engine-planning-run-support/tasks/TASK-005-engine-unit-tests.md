---
id: TASK-005
type: Task
title: Engine unit tests for planning runs
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-003-engine-planning-run-support/epic.md
---

# TASK-005 — Engine Unit Tests for Planning Runs

---

## Purpose

Add unit tests for `StartPlanningRun()` and the branch-aware precondition changes.

---

## Deliverable

Tests in `internal/engine/run_test.go` and `internal/engine/step_test.go`:

**StartPlanningRun tests:**
- Happy path: artifact created on branch, run active, mode=planning
- Invalid content: returns validation error
- Missing artifact content: returns ErrInvalidParams
- No workflow for type: returns ErrNotFound
- ArtifactWriter not configured: returns ErrUnavailable
- Branch creation failure: graceful handling

**Precondition tests:**
- `resolveReadRef` returns branch name for planning runs
- `resolveReadRef` returns "HEAD" for standard runs
- Precondition reads from branch during planning run

---

## Acceptance Criteria

- Tests follow existing mock patterns in `run_test.go`
- All new tests pass
- All existing engine tests continue to pass
- Coverage includes error paths
