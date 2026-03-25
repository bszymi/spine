---
id: TASK-001
type: Task
title: Validation in Workflow Progression
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-006-validation-integration/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-006-validation-integration/epic.md
---

# TASK-001 — Validation in Workflow Progression

## Purpose

Wire the validation engine into the engine orchestrator so that workflow steps with validation preconditions invoke cross-artifact validation before activation.

## Deliverable

- Implement `cross_artifact_valid` precondition type in engine orchestrator
- When a step has this precondition, invoke the validation engine before activating
- Validation results determine whether the step can proceed
- Validation errors are attached to the step execution record

## Acceptance Criteria

- Steps with `cross_artifact_valid` precondition invoke the validation engine
- Validation pass allows step activation
- Validation failure blocks step activation with error detail
- Validation results are persisted on the step execution record
- Steps without validation preconditions are unaffected
