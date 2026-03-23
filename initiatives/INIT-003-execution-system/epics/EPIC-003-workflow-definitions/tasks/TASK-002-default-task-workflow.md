---
id: TASK-002
type: Task
title: Default Task Workflow Definition
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-003-workflow-definitions/epic.md
---

# TASK-002 — Default Task Workflow Definition

## Purpose

Create the default workflow for standard implementation tasks. This is the first real workflow and the one used for the first working slice.

## Deliverable

`workflows/task-default.yaml` with steps:

1. `draft` — initial setup, preconditions checked
2. `execute` — actor performs the work, produces artifacts
3. `review` — reviewer evaluates the deliverable
4. `commit` — outcomes committed to Git

Each step must define: type, execution mode, preconditions, required_inputs/outputs, outcomes with next_step routing, timeout, and retry configuration.

## Acceptance Criteria

- Workflow parses successfully with existing workflow parser
- Workflow validates against schema and semantic rules
- `applies_to: [Task]` binds to all task types by default
- Steps cover the complete happy path (draft → execute → review → commit)
- Review step supports outcomes: accepted_to_continue, needs_rework
- Workflow is executable by the engine orchestrator
