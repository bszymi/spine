---
id: EPIC-004
type: Epic
title: "Multi-Repo Run Lifecycle"
status: Pending
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
owner: bszymi
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-003-git-client-pool-and-routing/epic.md
---

# EPIC-004 - Multi-Repo Run Lifecycle

---

## Purpose

Create and route run branches across the primary Spine repository and every code repository affected by a task.

This epic makes execution multi-repo aware before merge coordination is added.

---

## Scope

### In Scope

- Run model updates for affected repositories
- Branch creation across all affected repositories
- Cleanup when branch creation fails partway through
- Step repository routing and runner clone instructions
- Actor-facing assignment payload updates

### Out of Scope

- Final merge coordination
- Cross-repo atomic transactions
- Cross-repo divergence

---

## Primary Outputs

- Multi-repo run startup path
- Repository-aware step execution context
- Assignment payloads with clone URL, repo ID, and branch name
- Tests for startup, routing, and cleanup behavior

---

## Acceptance Criteria

1. Starting a run creates the same branch name in every affected repository.
2. A branch-creation failure cleans up already-created branches.
3. Step execution payloads identify the target repository.
4. Single-repo tasks keep the current behavior.
5. Runner containers can clone the intended repo and branch.

