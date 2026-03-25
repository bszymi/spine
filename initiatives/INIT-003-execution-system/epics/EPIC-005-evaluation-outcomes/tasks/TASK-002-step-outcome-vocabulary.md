---
id: TASK-002
type: Task
title: Step Outcome Vocabulary and Routing
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-005-evaluation-outcomes/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-005-evaluation-outcomes/epic.md
---

# TASK-002 — Step Outcome Vocabulary and Routing

## Purpose

Implement the distinct step-level outcome vocabulary and ensure outcomes route correctly through the engine orchestrator.

## Deliverable

- Step outcome constants: `accepted_to_continue`, `needs_rework`, `failed`
- Rework routing: `needs_rework` returns execution to a previous step (configurable per workflow)
- Integration with engine orchestrator's step progression
- Clear distinction from task-level outcomes in code and API

## Acceptance Criteria

- `accepted_to_continue` routes to the next step
- `needs_rework` routes back to the configured rework step
- `failed` triggers step failure handling (retry or permanent failure)
- Step outcomes and task outcomes use distinct vocabulary (no ambiguity)
- Rework loops are bounded (prevent infinite rework cycles)
