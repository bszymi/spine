---
id: TASK-003
type: Task
title: Add partial merge run state
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/tasks/TASK-002-code-repo-first-merge-order.md
---

# TASK-003 - Add Partial Merge Run State

---

## Purpose

Represent the case where some repositories have merged and others still require intervention.

## Deliverable

Update run status/state-machine documentation, domain constants, migrations, and scheduler logic for a partial merge state.

Behavior:

- A run enters partial merge when at least one repo merged and at least one failed.
- Merged repos are not retried.
- Failed repos remain eligible for retry after manual resolution.
- Completion requires all repos to be merged.

## Acceptance Criteria

- Partial merge is a valid non-terminal run state.
- Scheduler can resume partial merge runs.
- API responses expose partial merge status and outcomes.
- Tests cover transition into and out of partial merge.

