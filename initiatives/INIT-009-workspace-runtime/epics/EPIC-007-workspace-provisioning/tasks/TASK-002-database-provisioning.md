---
id: TASK-002
type: Task
title: Database provisioning
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-007-workspace-provisioning/epic.md
---

# TASK-002 — Database provisioning

---

## Purpose

Automate the creation of a new PostgreSQL database for a workspace and run all Spine schema migrations against it. Per [data-model.md §7.2](/architecture/data-model.md), each workspace gets its own database with runtime and projection schemas.

## Deliverable

`internal/workspace/provision.go` (or similar)

Content should define:

- A provisioning function that:
  1. Connects to the PostgreSQL server using a admin/provisioning connection string
  2. Creates a new database named after the workspace (e.g., `spine_ws_<workspace_id>`)
  3. Runs all workspace migrations from `migrations/` against the new database
  4. Returns the database URL for the new workspace
- Rollback: if migration fails, drop the created database
- The admin connection string comes from `SPINE_PROVISIONING_DATABASE_URL` (a connection with CREATE DATABASE privileges, typically to the `postgres` database)

## Acceptance Criteria

- A new PostgreSQL database is created for the workspace
- All Spine migrations (runtime + projection schemas) are applied
- The returned database URL is ready for use by the workspace's services
- Failed provisioning cleans up the partially created database
- Integration test demonstrates database creation and migration
