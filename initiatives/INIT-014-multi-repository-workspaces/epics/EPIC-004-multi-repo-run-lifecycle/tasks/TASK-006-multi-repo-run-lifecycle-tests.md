---
id: TASK-006
type: Task
title: Multi-repo run lifecycle tests
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-005-runner-clone-context.md
---

# TASK-006 - Multi-Repo Run Lifecycle Tests

---

## Purpose

Validate run startup and assignment behavior for tasks that span multiple repositories.

## Deliverable

Add unit and scenario tests for the multi-repo run lifecycle.

Coverage:

- Primary-only task.
- Single code repo task.
- Multiple code repo task.
- Branch creation cleanup on failure.
- Explicit step routing.
- Ambiguous step routing.
- Runner clone context.

## Acceptance Criteria

- Tests prove branch creation happens in every affected repo.
- Failure scenarios leave no orphaned startup state.
- Assignment payloads are stable and documented by tests.
- Existing run lifecycle tests remain valid.
