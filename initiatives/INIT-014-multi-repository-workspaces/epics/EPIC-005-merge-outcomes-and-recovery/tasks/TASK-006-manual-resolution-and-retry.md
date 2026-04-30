---
id: TASK-006
type: Task
title: Operator manual-resolution and retry path
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/tasks/TASK-003-partial-merge-run-state.md
---

# TASK-006 - Operator Manual-Resolution and Retry Path

---

## Purpose

Deliver the operator-facing surface that TASK-003's "failed repos remain eligible for retry after manual resolution" actually depends on. Without this task, a partial-merge run has no documented way to move forward.

## Deliverable

Implement the API, CLI, and engine behavior that lets a workspace operator mark a failed code-repo merge as resolved (off-spine, e.g. via manual force-merge or rebase in the upstream Git host) and request a retry of the run's pending merges.

The implementation should cover:

- API/CLI command to mark a failed per-repo outcome as `resolved-externally`, with an audit reason recorded in the primary repo ledger.
- API/CLI command to retry the merge for a specific failed repo without touching already-merged repos.
- Engine behavior that re-enters the merge phase only for repos in `failed` or `resolved-externally` state.
- Primary-repo ledger entries that record who resolved/retried, when, and why.
- Authorization gated by existing workspace-level RBAC.

## Acceptance Criteria

- Operator can mark a failed merge as externally resolved through API and CLI; ledger captures actor, reason, and target commit SHA.
- Operator can retry merge for a single failed repo; merged repos are not re-merged.
- Run completes once every affected repo is in a terminal `merged` or `resolved-externally` state.
- Unauthorized actors cannot resolve or retry.
- Audit entries are queryable from primary-repo history.
- Unit and scenario tests cover resolve-then-retry, retry-without-resolve, and double-resolve idempotency.
- Structured logs/metrics record resolution and retry events for observability.
