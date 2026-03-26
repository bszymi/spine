---
id: TASK-003
type: Task
title: CLI Workflow Commands
status: In Progress
epic: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-010-developer-experience/epic.md
---

# TASK-003 — CLI Workflow Commands

## Purpose

Implement workflow management commands so developers can list available workflows and check which workflow would bind to a given artifact.

## Deliverable

- `spine workflow list` — list all available workflow definitions with status
- `spine workflow resolve [artifact-path]` — show which workflow would bind to the given artifact
- `spine workflow show [workflow-path]` — display workflow definition details (steps, preconditions, outcomes)

## Acceptance Criteria

- `workflow list` shows all workflows with id, name, version, status, applies_to
- `workflow resolve` shows the binding result for a given artifact (or error if ambiguous)
- `workflow show` displays the full workflow structure in a readable format
- Commands work with both table and JSON output
