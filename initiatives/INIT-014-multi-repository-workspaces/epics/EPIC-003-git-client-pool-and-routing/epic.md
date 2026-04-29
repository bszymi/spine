---
id: EPIC-003
type: Epic
title: "Git Client Pool and Repository Routing"
status: Completed
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
---

# EPIC-003 - Git Client Pool and Repository Routing

---

## Purpose

Replace single-repository Git wiring with explicit per-repository Git client resolution.

The primary client remains the default for governance services. Code repository clients are resolved only when execution, git HTTP serving, or merge coordination needs them.

---

## Scope

### In Scope

- `GitClientPool` interface and implementation
- Per-repository clone and lazy client initialization
- Workspace service wiring changes
- Git HTTP route extension to `/git/{workspace_id}/{repo_id}`
- Path traversal and inactive-repository protections

### Out of Scope

- Run branch lifecycle across repos
- Per-repo credential delegation by actor
- Code repository projection

---

## Primary Outputs

- Repository-aware Git client pool
- Gateway route parsing for workspace and repository IDs
- Service wiring that preserves existing single-repo behavior
- Unit and scenario tests for routing and client resolution

---

## Acceptance Criteria

1. Existing services can still use the primary Git client unchanged.
2. Execution code can resolve a Git client by repository ID.
3. `/git/{workspace_id}/{repo_id}` serves the selected repository.
4. `/git/{workspace_id}` remains a primary-repo fallback.
5. Invalid repository IDs cannot escape configured workspace paths.

