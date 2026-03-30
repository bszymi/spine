---
id: TASK-002
type: Task
title: Workflow parse and validation tests
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

# TASK-002 — Workflow Parse and Validation Tests

---

## Purpose

Ensure `initiative-lifecycle.yaml` is included in the workflow reference test suite and parses correctly.

---

## Deliverable

Add `initiative-lifecycle.yaml` to existing workflow reference tests (e.g., in `internal/workflow/` test files or scenario tests that validate all workflows parse).

---

## Acceptance Criteria

- Workflow parses without errors
- Step IDs, outcomes, and transitions are valid
- `applies_to` resolves correctly for `Initiative` type
- Existing workflow tests continue to pass
