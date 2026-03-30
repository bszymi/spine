---
id: TASK-002
type: Task
title: Add workflow parse and validation tests
status: Draft
epic: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
initiative: /initiatives/INIT-006-governed-artifact-creation/initiative.md
work_type: implementation
created: 2026-03-30
last_updated: 2026-03-30
links:
  - type: parent
    target: /initiatives/INIT-006-governed-artifact-creation/epics/EPIC-005-workflow-definitions/epic.md
---

# TASK-002 — Add Workflow Parse and Validation Tests

---

## Purpose

Ensure `artifact-creation.yaml` is included in the workflow reference test suite, the `mode` field parses correctly, and there are no binding conflicts with existing execution workflows.

---

## Deliverable

Tests in `workflows/reference_workflows_test.go` and `internal/workflow/`:

- `artifact-creation.yaml` parses without errors
- `mode` field is read as `"creation"`
- `applies_to` includes all expected types (Initiative, Epic, Task, Product, ADR)
- Step IDs, outcomes, and transitions are valid
- No binding conflict: `TestNoBindingConflicts` passes — the `mode` field disambiguates `artifact-creation` (mode: creation) from `task-default` (mode: execution) for the `Task` type
- Existing execution workflows default to `mode: execution` when the field is absent

---

## Acceptance Criteria

- `artifact-creation.yaml` is in the reference workflow test suite
- `mode` field parsing is tested explicitly
- No binding conflicts detected
- Existing workflow tests continue to pass (backward compatible — absent `mode` defaults to `execution`)
