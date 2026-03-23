---
id: TASK-003
type: Task
title: Workflow Binding Integration
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
---

# TASK-003 — Workflow Binding Integration

## Purpose

Wire `ResolveBinding` into run creation so that runs are automatically bound to the correct workflow based on artifact type and work_type.

## Deliverable

- Wire `workflow.ResolveBinding` into engine orchestrator's `StartRun`
- Implement work_type filtering in applies_to matching
- Set `WorkflowPath` and `WorkflowID` on API-created runs (currently empty)
- Pin resolved workflow version (commit SHA) to the run

## Acceptance Criteria

- `StartRun` resolves the correct workflow without explicit workflow path
- Work-type filtering selects the appropriate workflow variant (e.g., spike vs default)
- Exactly one workflow matches per artifact type + work_type; zero or multiple = error
- Workflow version is pinned at run creation time
- `WorkflowPath` and `WorkflowID` are populated on all runs
