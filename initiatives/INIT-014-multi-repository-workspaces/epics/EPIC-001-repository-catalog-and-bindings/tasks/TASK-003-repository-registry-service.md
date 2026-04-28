---
id: TASK-003
type: Task
title: Implement repository registry service
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-002-runtime-repository-binding-schema.md
---

# TASK-003 - Implement Repository Registry Service

---

## Purpose

Provide one service that resolves repository catalog entries and runtime bindings into usable repository configuration.

## Deliverable

Create a repository registry package, likely under `internal/repository` or `internal/workspace`.

Responsibilities:

- Parse the governed catalog from the primary repo.
- Merge catalog identity with runtime binding details.
- Return active repository configs by ID.
- Enforce ID, kind, and primary-repo invariants.
- Expose list and lookup operations to gateway, engine, and git HTTP routing.

## Acceptance Criteria

- Lookup of `spine` always resolves to the workspace primary repo.
- Lookup of a code repo requires both catalog identity and active runtime binding.
- Unknown IDs return a typed not-found error.
- Inactive bindings return a typed inactive error.
- Unit tests cover catalog-only, binding-only, and mismatched states.

