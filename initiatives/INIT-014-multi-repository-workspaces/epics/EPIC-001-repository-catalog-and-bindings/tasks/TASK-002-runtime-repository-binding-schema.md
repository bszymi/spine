---
id: TASK-002
type: Task
title: Add runtime repository binding schema
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-001-governed-repository-catalog-format.md
---

# TASK-002 - Add Runtime Repository Binding Schema

---

## Purpose

Store operational repository connection details outside Git while preserving a link to the governed repository ID.

## Deliverable

Add workspace database migrations and store types for repository bindings.

The schema should track:

- `repository_id`
- `workspace_id`
- Clone URL or secret reference
- Credential secret reference
- Local path
- Default branch override if needed
- Status: `active` or `inactive`
- Timestamps

## Acceptance Criteria

- Binding rows are keyed by workspace and repository ID.
- Operational fields are not projected from Git artifacts.
- Inactive bindings cannot be resolved for execution.
- The primary `spine` binding is resolved virtually from existing workspace state (RepoPath, default branch) — **no row is written** for primary in any existing or new workspace. This must hold so the EPIC-001 TASK-006 backward-compatibility scenario passes.
- Store methods cover create, read, update, list, and deactivate for code repository bindings only.
- A migration that backfills primary-repo binding rows is explicitly out of scope.

