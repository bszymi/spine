---
id: TASK-002
type: Task
title: Cross-Artifact Blocking Conditions
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-006-validation-integration/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-006-validation-integration/epic.md
---

# TASK-002 — Cross-Artifact Blocking Conditions

## Purpose

Implement blocking conditions that prevent workflow progression when cross-artifact integrity is violated, with clear classification of the violation type.

## Deliverable

- Blocking logic: validation failures of severity `error` block progression
- Validation warnings allow progression but are logged
- Violation classification surfaced in API responses (scope_conflict, architectural_conflict, implementation_drift, missing_prerequisite)
- Integration with run status: blocked steps show validation failure reason

## Acceptance Criteria

- Validation errors block step progression
- Validation warnings are logged but do not block
- Violation types are classified per the validation service spec
- Blocked steps show the specific validation failure in their status
- Actors can see why a step is blocked via the API
