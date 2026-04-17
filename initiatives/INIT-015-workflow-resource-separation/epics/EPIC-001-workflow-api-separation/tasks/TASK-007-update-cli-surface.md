---
id: TASK-007
type: Task
title: "Update CLI Surface to Use workflow.* Operations"
status: Pending
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

# TASK-007 — Update CLI Surface to Use workflow.* Operations

---

## Context

The CLI currently writes workflow definitions through the generic artifact operations. Once TASK-005 lands, those code paths will return 400. The CLI must target the new `workflow.*` operations instead.

## Deliverable

- Identify every CLI command that creates, updates, or validates workflow definitions (e.g. subcommands under `spine workflow` or equivalent, plus any generic `spine artifact` paths that accept workflow targets).
- Route each to the new API operations. Remove any "workflow mode" branches from generic artifact CLI commands.
- Update CLI integration tests to exercise the new endpoints.
- Update help text and any CLI reference doc produced from the tree.

## Acceptance Criteria

- No CLI command writes a workflow definition through a generic artifact endpoint.
- Existing CLI behavior (create / update / list / read / validate) works end-to-end against the new endpoints.
- CLI integration tests cover the updated code paths.
