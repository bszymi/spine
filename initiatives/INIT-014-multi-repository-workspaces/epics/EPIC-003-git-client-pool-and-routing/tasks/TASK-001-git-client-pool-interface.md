---
id: TASK-001
type: Task
title: Define Git client pool interface
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-003-repository-registry-service.md
---

# TASK-001 - Define Git Client Pool Interface

---

## Purpose

Introduce a small abstraction for resolving Git clients by repository ID.

## Deliverable

Add a pool interface and implementation around existing `git.GitClient`.

Required methods:

- `PrimaryClient() git.GitClient`
- `Client(ctx, repositoryID) (git.GitClient, error)`
- `RepositoryPath(ctx, repositoryID) (string, error)`
- `ListActive(ctx) ([]Repository, error)`

## Acceptance Criteria

- Existing code can continue accepting a primary `git.GitClient`.
- New code can resolve clients by repo ID.
- Unknown and inactive repository errors are typed.
- Pool construction works in single-workspace mode.
- Unit tests cover primary and code repo lookup.

