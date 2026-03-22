---
id: TASK-002
type: Task
title: Workflow Engine Integration
status: Completed
epic: /initiatives/INIT-002-implementation/epics/EPIC-006-validation-service/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-006-validation-service/epic.md
---

# TASK-002 — Workflow Engine Integration

## Purpose

Integrate the Validation Service with the Workflow Engine so `cross_artifact_valid` preconditions trigger validation during workflow execution.

## Deliverable

- `cross_artifact_valid` precondition evaluation calls Validation Service
- Validation result determines step progression (pass → proceed, fail → block)
- Warnings are logged and emitted as events but do not block
- `system.validate_all` operation implementation
- Validation events emitted for observability

## Acceptance Criteria

- A step with `cross_artifact_valid` precondition is blocked when validation fails
- A step with passing validation proceeds normally
- Warnings are captured in step execution metadata
- `system.validate_all` returns a complete report for all artifacts
- Integration tests: workflow step blocked by validation failure, then unblocked after fix
