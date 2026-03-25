---
id: TASK-001
type: Task
title: Workflow Directory and Authoring Patterns
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
---

# TASK-001 — Workflow Directory and Authoring Patterns

## Purpose

Create the `workflows/` directory and establish the conventions for authoring workflow definitions.

## Deliverable

- `workflows/` directory in repository root
- Update repository-structure.md to include workflows directory
- Document authoring patterns: naming conventions, versioning rules, applies_to usage
- Update projection service discovery to scan workflows directory

## Acceptance Criteria

- `workflows/` directory exists and is recognized by the system
- Projection service discovers and parses workflow files from this directory
- Naming convention is documented and consistent
- Workflow files use the schema defined in Workflow Definition Format
