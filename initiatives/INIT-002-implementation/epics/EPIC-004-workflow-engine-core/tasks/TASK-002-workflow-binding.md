---
id: TASK-002
type: Task
title: Workflow Binding Resolution
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-004-workflow-engine-core/epic.md
---

# TASK-002 — Workflow Binding Resolution

## Purpose

Implement the workflow binding resolution algorithm that matches artifacts to workflow definitions.

## Deliverable

- Resolution by `(type, work_type)` pair (per Task-Workflow Binding §4)
- Specific `work_type` takes precedence over general match
- Git SHA pinning at Run creation
- Error on ambiguous or missing binding

## Acceptance Criteria

- Resolution finds the correct Active workflow for a given artifact type
- `work_type` refinement selects the specific workflow when available
- Falls back to general match when no `work_type` match exists
- Returns error when no Active workflow matches
- Returns error when multiple workflows conflict on same `(type, work_type)`
- Resolved workflow is pinned to a Git commit SHA
- Unit tests cover all resolution scenarios from Task-Workflow Binding §4
