---
id: TASK-002
type: Task
title: Update data-model.md storage guidance
status: Completed
epic: /initiatives/INIT-009-workspace-runtime/epics/EPIC-002-architecture-update/epic.md
initiative: /initiatives/INIT-009-workspace-runtime/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-009-workspace-runtime/epics/EPIC-002-architecture-update/epic.md
  - type: related_to
    target: /architecture/data-model.md
---

# TASK-002 — Update data-model.md storage guidance

---

## Purpose

Update the storage technology guidance section of `architecture/data-model.md` to describe per-workspace database isolation and the workspace registry database.

## Deliverable

Updates to `architecture/data-model.md` §7.

Content should describe:

- Each workspace has its own PostgreSQL database (runtime + projection schemas)
- The workspace registry is a separate coordination database (or config file in single mode) that maps workspace IDs to database connection strings and repo paths
- No shared tables with workspace_id partitioning — isolation is at the connection level
- The existing single-database model is a special case (one workspace, one database)

## Acceptance Criteria

- Per-workspace database isolation strategy is documented
- The workspace registry database concept is introduced
- The relationship between the registry and workspace databases is clear
- v0.x single-database description is retained as the single-mode case
