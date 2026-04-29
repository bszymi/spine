---
id: TASK-001
type: Task
title: Define per-repository merge outcome model
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: design
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-001-run-affected-repositories-model.md
---

# TASK-001 - Define Per-Repository Merge Outcome Model

---

## Purpose

Represent merge progress separately for every repository affected by a run.

## Deliverable

Define domain and storage shape for merge outcomes.

Outcome fields should include:

- Repository ID
- Status: `pending`, `merged`, `failed`, `skipped`, `resolved-externally`
- Source branch
- Target branch
- Merge commit SHA
- Error classification
- Error detail
- Timestamps
- Resolver / retry audit fields (actor, reason) for `resolved-externally` and retried outcomes

## Acceptance Criteria

- Outcome model supports partial merge states.
- Outcome data can be persisted and queried by run ID.
- Outcome statuses are documented.
- Model distinguishes transient and permanent failures.
- Primary repo outcome can record ledger commit details.
- Schema includes the audit fields needed by the EPIC-005 TASK-006 manual-resolution path.
- Per-outcome metrics/log fields are defined so observability dashboards can break down success/failure by repo without inferring it from prose.

