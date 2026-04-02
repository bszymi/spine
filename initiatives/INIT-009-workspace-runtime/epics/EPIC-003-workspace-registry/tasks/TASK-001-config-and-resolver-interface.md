---
id: TASK-001
type: Task
title: Workspace config type and resolver interface
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
---

# TASK-001 — Workspace config type and resolver interface

---

## Purpose

Define the core types that all workspace-aware runtime behavior builds on. See [data-model.md §7.2](/architecture/data-model.md) for the workspace registry field definitions and [components.md §6.5](/architecture/components.md) for how the resolver fits into the runtime.

## Deliverable

`internal/workspace/config.go` and `internal/workspace/resolver.go`

Content should define:

- `WorkspaceConfig` struct: workspace ID, display name, database URL, repo path, status, actor/auth scope (so per-workspace actor isolation can be initialized from config)
- `WorkspaceResolver` interface: `Resolve(ctx, workspaceID) (*WorkspaceConfig, error)` and `List(ctx) ([]WorkspaceConfig, error)`
- Error types: `ErrWorkspaceNotFound`, `ErrWorkspaceInactive`

## Acceptance Criteria

- Types are defined in `internal/workspace/` package
- `WorkspaceConfig` contains all fields needed to initialize a complete service set
- `WorkspaceResolver` is an interface implementable by both file/env and database providers
