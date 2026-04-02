---
id: TASK-004
type: Task
title: Batch migration scenario test
status: Pending
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-008-scenario-tests/epic.md
---

# TASK-004 — Batch migration scenario test

---

## Purpose

Verify that `spine migrate --all-workspaces` correctly applies pending migrations to all workspace databases and the registry database.

## Deliverable

New scenario test or integration test.

Test flow:
1. Set up a registry with 3 active workspaces, each with its own database
2. Verify all databases have migrations up to version N
3. Add a new migration file (version N+1)
4. Run batch migration
5. Verify all 3 workspace databases now have version N+1 in schema_migrations
6. Verify the registry database also has its migrations applied
7. Test partial failure: make one workspace DB unreachable, verify other 2 still migrate successfully

## Acceptance Criteria

- All workspace databases receive pending migrations
- Registry database is also migrated
- Partial failure doesn't prevent other workspaces from being migrated
- schema_migrations table correctly tracks applied versions
