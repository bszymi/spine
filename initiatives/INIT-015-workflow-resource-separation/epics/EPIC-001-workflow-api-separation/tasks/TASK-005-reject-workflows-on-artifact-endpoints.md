---
id: TASK-005
type: Task
title: "Reject Workflow Targets on Generic Artifact Endpoints"
status: Completed
work_type: implementation
created: 2026-04-17
epic: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
initiative: /initiatives/INIT-015-workflow-resource-separation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
  - type: blocked_by
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/tasks/TASK-004-implement-workflow-handlers.md
---

# TASK-005 — Reject Workflow Targets on Generic Artifact Endpoints

---

## Context

Per [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md), generic artifact write operations must refuse to write workflow definitions once the dedicated endpoints exist.

## Deliverable

- Add a single `isWorkflowTarget` helper (path prefix `/workflows/` and/or declared type) used by the `artifact.create`, `artifact.update`, and `artifact.add` handlers.
- Each of these handlers returns `400 invalid_params` with an error payload pointing to the corresponding `workflow.*` operation when the target is a workflow.
- Update `artifact.read` against a workflow path to return the same summary projection as `query.artifacts` — not the executable body.
- Add handler-level tests for rejection on each affected operation.
- Add an integration test that asserts every write path rejects a workflow target and that the error payload names the correct `workflow.*` operation.

## Acceptance Criteria

- All three generic artifact write operations reject workflow targets; tests cover each path.
- `artifact.read` on a workflow path returns the summary projection only.
- No code path in the generic artifact handlers invokes the workflow validation suite directly.
