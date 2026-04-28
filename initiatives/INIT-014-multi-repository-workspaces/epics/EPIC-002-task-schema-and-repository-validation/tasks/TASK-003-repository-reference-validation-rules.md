---
id: TASK-003
type: Task
title: Add repository reference validation rules
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/tasks/TASK-002-project-task-repositories.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-001-repository-catalog-and-bindings/tasks/TASK-003-repository-registry-service.md
---

# TASK-003 - Add Repository Reference Validation Rules

---

## Purpose

Detect task-to-repository mismatches at validate time, before any run is started.

This task covers static, catalog-existence checks against committed governance artifacts. Runtime binding state (active/inactive) is intentionally **not** checked here — that is run-start precondition territory and lives in TASK-004.

## Deliverable

Extend the validation service with repository reference checks against the governed catalog.

Rules should include:

- Repository IDs referenced by a Task must resolve to either the implicit primary `spine` repository or an entry in `/.spine/repositories.yaml`.
- The primary `spine` repository ID is always considered to exist, even when no catalog file is committed (single-repo workspaces).
- Duplicate repository IDs in one Task are invalid.
- Reserved ID rules are enforced: `spine` cannot be declared in the catalog as a `code` kind, and code repos cannot reuse the `spine` ID.
- Repository ID syntax matches the catalog format (lowercase alphanumeric with hyphens).

## Acceptance Criteria

- Unknown repo IDs produce validation errors.
- Duplicate repo IDs are rejected.
- Reserved ID violations (catalog redeclaration of `spine`) are rejected.
- An explicit `repositories: [spine]` validates in a single-repo workspace with no catalog file.
- Missing `repositories` remains valid (defaults to primary repo).
- Validation result messages name the task path and repository ID.
- Runtime active/inactive state is **not** consulted by this task — covered by TASK-004.
- Validation logs include the catalog snapshot ref so failures are reproducible.

