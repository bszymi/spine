---
id: TASK-001
type: Task
title: "Document Workflow Definition Operations in api-operations.md"
status: Completed
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

# TASK-001 — Document Workflow Definition Operations in api-operations.md

---

## Context

[ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) introduces a new operation category for workflow definitions. [api-operations.md](/architecture/api-operations.md) currently documents workflow writes implicitly through the Artifact Operations table, which contradicts the ADR.

## Deliverable

Update `/architecture/api-operations.md`:

- Add a new §3.x **Workflow Definition Operations** subsection describing `workflow.create`, `workflow.update`, `workflow.read`, `workflow.list`, and `workflow.validate`, with a domain-rules block covering validation suite, authorization requirement, and version-bump requirement on update.
- Annotate §3.1 Artifact Operations so it explicitly excludes workflow definitions; callers targeting workflow paths receive `400 invalid_params` pointing to the `workflow.*` operations.
- Update §3.2 to cross-reference the new operation category where workflow binding resolution is discussed.
- Add a cross-reference entry to [ADR-007](/architecture/adr/ADR-007-workflow-resource-separation.md) in §8.

## Acceptance Criteria

- `workflow.*` operations are fully documented with `operation`, `effect`, and `when to use` columns mirroring other categories.
- §3.1 no longer implies workflow writes are a valid use of generic artifact operations.
- ADR-007 is referenced in §8 Cross-References.
