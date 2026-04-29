---
id: TASK-005
type: Task
title: Git routing and pool tests
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/tasks/TASK-004-extend-git-http-repository-routing.md
---

# TASK-005 - Git Routing and Pool Tests

---

## Purpose

Pin repository routing behavior before run lifecycle changes start depending on it.

## Deliverable

Add focused unit and scenario tests for the Git client pool and git HTTP routing.

Coverage:

- Primary fallback route.
- Explicit code repository route.
- Unknown repository.
- Inactive repository.
- Push path with per-workspace policy.
- Concurrent lazy initialization.

## Acceptance Criteria

- Unit tests cover route parsing edge cases.
- Pool tests cover cache and clone behavior.
- Git HTTP scenario tests prove clone works for code repos.
- Existing git HTTP tests remain valid.
