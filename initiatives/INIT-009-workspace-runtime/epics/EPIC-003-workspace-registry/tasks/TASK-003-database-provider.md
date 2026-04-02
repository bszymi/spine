---
id: TASK-003
type: Task
title: Database workspace provider
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/tasks/TASK-001-config-and-resolver-interface.md
---

# TASK-003 — Database workspace provider

---

## Purpose

Implement the `WorkspaceResolver` backed by a workspace registry table in PostgreSQL. See [data-model.md §7.2](/architecture/data-model.md) for the registry schema.

## Deliverable

`internal/workspace/db_provider.go` and a database migration.

Content should define:

- A `workspace_registry` table: workspace ID (PK), display name, database URL, repo path, status (active/inactive), created_at, updated_at
- A resolver that queries this table by workspace ID
- Caching layer: resolved configs cached in memory with configurable TTL (default: 60s)
- `List()` returns all active workspaces
- Migration file in `migrations/`

## Acceptance Criteria

- Implements `WorkspaceResolver` interface
- Registry table schema is defined and migrated
- Cache refreshes after configurable TTL
- Inactive workspaces are not resolved (treated as not found)
- Unit tests cover: resolve from DB, resolve from cache, cache expiry, unknown workspace, inactive workspace
