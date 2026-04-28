---
id: TASK-005
type: Task
title: Task repository validation tests
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/tasks/TASK-004-run-start-repository-preconditions.md
---

# TASK-005 - Task Repository Validation Tests

---

## Purpose

Cover the schema, projection, validation, and run-start behavior for task repository bindings.

## Deliverable

Add tests across artifact, projection, validation, gateway, and engine packages.

Scenarios:

- Existing task with no `repositories`.
- Task with one code repository.
- Task with multiple code repositories.
- Task with unknown repository.
- Task with inactive repository.
- Task with duplicate repository IDs.

## Acceptance Criteria

- Tests pin backward-compatible default behavior.
- Tests prove validation errors are surfaced before branch creation.
- Projection tests verify repository metadata is queryable.
- Scenario test starts a run for a valid multi-repo task.
