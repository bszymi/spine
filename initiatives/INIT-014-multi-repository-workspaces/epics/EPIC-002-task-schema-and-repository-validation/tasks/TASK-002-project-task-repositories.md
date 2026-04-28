---
id: TASK-002
type: Task
title: Project task repository bindings
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/tasks/TASK-001-task-repositories-frontmatter-schema.md
---

# TASK-002 - Project Task Repository Bindings

---

## Purpose

Make task repository bindings queryable without reparsing Markdown on every request.

## Deliverable

Extend projection and query code so Task repository IDs are available from projected metadata and execution projections.

Expected behavior:

- Projection stores the raw `repositories` list in artifact metadata.
- Execution projection exposes affected repositories for task discovery.
- Query output can include repository IDs for task views.

## Acceptance Criteria

- Projection sync preserves task repository metadata.
- Execution projection can return affected repositories.
- Query tests cover missing, empty, single, and multiple repository lists.
- No code repository content is projected.

