---
id: TASK-005
type: Task
title: Batch workspace migration
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
  - type: depends_on
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-003-workspace-registry/epic.md
---

# TASK-005 — Batch workspace migration

---

## Purpose

When a new Spine version includes new database migrations, all existing workspace databases must be updated. Today, `spine migrate` only runs against a single `SPINE_DATABASE_URL`. There is no way to apply migrations across all workspace databases in shared mode.

Without this, upgrading Spine in shared mode requires manually running migrations against each workspace database — error-prone and unscalable.

## Deliverable

Updates to `cmd/spine/main.go` (migrate command) and potentially a new function in `internal/workspace/`.

Content should define:

- `spine migrate --all-workspaces` flag on the existing migrate command
- When set, the command:
  1. Connects to the workspace registry database (`SPINE_REGISTRY_DATABASE_URL`)
  2. Lists all active workspaces
  3. For each workspace, connects to its database and runs `ApplyMigrations`
  4. Reports per-workspace success/failure
  5. Continues to the next workspace if one fails (does not abort on first error)
- Without the flag, `spine migrate` behaves exactly as today (single database)
- Also runs migrations against the registry database itself (in case `migrations/registry/` has new files)
- Consider: `spine serve` startup could optionally auto-migrate all workspaces (configurable, off by default)

## Acceptance Criteria

- `spine migrate --all-workspaces` applies pending migrations to all active workspace databases
- Each workspace's `schema_migrations` table is updated with newly applied versions
- A failure in one workspace does not prevent other workspaces from being migrated
- The command reports which workspaces were migrated and which failed
- Without `--all-workspaces`, the command behaves identically to current behavior
- Registry database migrations (`migrations/registry/`) are also applied
