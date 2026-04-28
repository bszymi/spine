---
id: TASK-004
type: Task
title: Clean up branches per repository after merge
status: Pending
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

# TASK-004 - Clean Up Branches Per Repository After Merge

---

## Purpose

Delete successful run branches without losing failed-merge branches needed for manual repair.

## Deliverable

Update branch cleanup to operate per repository.

Rules:

- Delete branch after successful merge in that repo.
- Preserve branch after failed merge in that repo.
- Deleting a branch in one repo must not affect other repos.
- Remote branch cleanup follows the same per-repo outcome rules.

## Acceptance Criteria

- Successful repo branches are deleted.
- Failed repo branches are preserved.
- Cleanup errors are recorded without marking a merge as failed.
- Tests cover mixed success and failure outcomes.

