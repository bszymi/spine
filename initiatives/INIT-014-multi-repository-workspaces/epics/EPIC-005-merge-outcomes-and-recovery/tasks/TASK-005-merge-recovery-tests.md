---
id: TASK-005
type: Task
title: Merge recovery tests and scenarios
status: Pending
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: testing
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/tasks/TASK-004-branch-cleanup-per-repository.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-005-merge-outcomes-and-recovery/tasks/TASK-006-manual-resolution-and-retry.md
---

# TASK-005 - Merge Recovery Tests and Scenarios

---

## Purpose

Prove independent merge, partial merge, and recovery behavior end to end.

## Deliverable

Add tests for multi-repo merge outcomes and scheduler recovery.

Scenarios:

- All repos merge successfully.
- One code repo fails before any merge.
- One code repo fails after another has merged.
- Primary repo ledger merge fails after code repos merge.
- Partial merge is retried after manual resolution.

## Acceptance Criteria

- Tests show successful repos are not re-merged on retry.
- Partial merge state is observable through API.
- Failed repo branches are preserved.
- Completed runs have merged outcomes for every affected repo.
- Existing single-repo merge tests remain valid.
