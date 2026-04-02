---
id: EPIC-003
type: Epic
title: Workspace Registry
status: Pending
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/initiative.md
  - type: related_to
    target: /architecture/components.md
  - type: related_to
    target: /architecture/data-model.md
---

# EPIC-003 — Workspace Registry

---

## Purpose

Implement the workspace resolver interface, configuration types, and both provider implementations (file/env and database). This is the foundation for all workspace-aware runtime behavior described in [components.md §6.5](/architecture/components.md) and [data-model.md §7.2](/architecture/data-model.md).

---

## Key Work Areas

- Define `WorkspaceConfig` struct and `WorkspaceResolver` interface
- Implement file/env provider — wraps current `SPINE_DATABASE_URL`, `SPINE_REPO_PATH` behind the resolver interface
- Implement database provider — workspace registry table with lookup and caching
- Database migration for the workspace registry table
- Startup provider selection based on configuration
- Config caching with TTL-based refresh for the database provider

---

## Primary Outputs

- `internal/workspace/config.go` — `WorkspaceConfig` type
- `internal/workspace/resolver.go` — `WorkspaceResolver` interface
- `internal/workspace/file_provider.go` — file/env-based resolver
- `internal/workspace/db_provider.go` — database-backed resolver
- Database migration for workspace registry table
- Unit tests for both providers

---

## Acceptance Criteria

- `WorkspaceResolver` interface is defined with `Resolve(ctx, workspaceID)` and `List(ctx)`
- File/env provider returns a single workspace config from current environment variables
- Database provider reads workspace config from a registry table with caching
- Both providers conform to the same interface and are interchangeable
- Spine startup selects the provider based on a configuration flag
- File/env provider produces identical runtime behavior to current Spine (backward compatible)
