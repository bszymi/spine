---
id: TASK-003
type: Task
title: Relax resolveWriteContext for planning runs
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-004-api-gateway-and-cli/epic.md
---

# TASK-003 — Relax resolveWriteContext for Planning Runs

---

## Purpose

Update `resolveWriteContext()` to allow any artifact to be written to a planning run's branch without requiring `task_path` validation.

Planning runs own the entire branch — users need to create multiple artifacts (initiative, epics, tasks) on the same branch. The strict `task_path` match is only needed for standard execution runs.

---

## Deliverable

`internal/gateway/handlers_artifacts.go`

Update `resolveWriteContext()`:
- Look up the run by `run_id`
- If `run.Mode == RunModePlanning`: return `run.BranchName` directly (skip `task_path` validation)
- If standard run: preserve existing behavior (`task_path` required and must match)

---

## Acceptance Criteria

- Planning runs: `write_context: { "run_id": "..." }` works without `task_path`
- Standard runs: existing `task_path` validation is unchanged
- Invalid `run_id` returns appropriate error for both modes
- Run must be in `active` status for writes to be allowed
