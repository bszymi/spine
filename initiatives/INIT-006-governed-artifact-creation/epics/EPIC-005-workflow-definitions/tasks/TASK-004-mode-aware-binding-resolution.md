---
id: TASK-004
type: Task
title: Add mode-aware workflow binding resolution
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
  - type: blocked_by
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/tasks/TASK-003-add-mode-field-to-parser.md
---

# TASK-004 — Add Mode-Aware Workflow Binding Resolution

---

## Purpose

Update the workflow binding resolver so that `StartPlanningRun()` resolves to `mode: creation` workflows while `StartRun()` continues to resolve to `mode: execution` workflows.

Without this, a planning run for a Task would incorrectly bind to `task-default.yaml` (execution) instead of `artifact-creation.yaml` (creation).

---

## Deliverable

`internal/workflow/` — update `ResolveWorkflow()` (or add an overload) to accept an optional mode filter:

- `ResolveWorkflow(ctx, artifactType, workType)` — existing signature, defaults to `mode: execution`
- `ResolveWorkflow(ctx, artifactType, workType, mode)` — or add a `ResolveWorkflowWithMode()` method

`StartPlanningRun()` calls the resolver with `mode: "creation"`.
`StartRun()` calls the resolver with `mode: "execution"` (or uses the default).

---

## Acceptance Criteria

- Planning runs resolve to `artifact-creation.yaml` for any artifact type
- Standard runs resolve to type-specific execution workflows (unchanged behavior)
- If no creation workflow exists for a type, planning run start returns `ErrNotFound`
- Backward compatible — existing `ResolveWorkflow()` callers continue to work without modification
