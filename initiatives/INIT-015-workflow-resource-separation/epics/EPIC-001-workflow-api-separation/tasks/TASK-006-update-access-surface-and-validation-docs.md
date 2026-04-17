---
id: TASK-006
type: Task
title: "Update Access Surface and Validation Service Docs"
status: Pending
work_type: documentation
created: 2026-04-17
epic: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
initiative: /initiatives/INIT-015-workflow-resource-separation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-015-workflow-resource-separation/epics/EPIC-001-workflow-api-separation/epic.md
  - type: related_to
    target: /architecture/adr/ADR-007-workflow-resource-separation.md
---

# TASK-006 — Update Access Surface and Validation Service Docs

---

## Context

The Access Surface and Validation Service architecture docs must reflect the new operation category and the fact that `workflow.validate` is a first-class callable of the validation service.

## Deliverable

- Update `/architecture/access-surface.md`:
  - Add the Workflow Definition Operation category to the operation taxonomy.
  - Ensure CLI and GUI surfaces route workflow writes to the new operations.
- Update `/architecture/validation-service.md`:
  - Document the workflow validation suite as an operation exposed to `workflow.create`, `workflow.update`, and `workflow.validate`.
  - Note that the generic artifact validation path does not invoke workflow-specific rules.
- Cross-link ADR-007 from both documents.

## Acceptance Criteria

- Both documents describe the new operation category and are consistent with `api-operations.md`.
- ADR-007 is referenced from both.
