---
id: TASK-004
type: Task
title: Enforce repository preconditions at run start
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-002-task-schema-and-repository-validation/tasks/TASK-003-repository-reference-validation-rules.md
---

# TASK-004 - Enforce Repository Preconditions at Run Start

---

## Purpose

Prevent a run from starting when its affected repositories cannot be resolved against runtime bindings.

This task owns all runtime-state checks (active/inactive bindings, clone reachability, local path init). Catalog-existence checks belong to TASK-003 and run earlier.

## Deliverable

Update `StartRun` or its surrounding gateway/service path to resolve affected repositories against runtime bindings before any branch is created.

The precondition should produce a typed error when:

- A repository is inactive at run start.
- Runtime binding is missing for a known catalog entry.
- Clone or local path initialization fails.
- Credential resolution for a code repo fails.

## Acceptance Criteria

- Run start fails before any branch is created when repository resolution fails.
- Error responses identify the failing repository ID and the failure category.
- Missing `repositories` starts a primary-repo-only run.
- Repository preconditions run after ordinary task blocking checks and after the validate-time catalog checks from TASK-003.
- Inactive-at-run-start is fully covered here (not in TASK-003).
- Structured logs/metrics record per-repo resolution outcomes for observability.
- Unit tests cover each failure mode.

