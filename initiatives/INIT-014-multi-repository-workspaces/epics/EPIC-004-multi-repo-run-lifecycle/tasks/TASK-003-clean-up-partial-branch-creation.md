---
id: TASK-003
type: Task
title: Clean up partial branch creation failures
status: Completed
epic: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
initiative: /initiatives/INIT-014-multi-repository-workspaces/initiative.md
work_type: implementation
created: 2026-04-28
links:
  - type: parent
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/epic.md
  - type: blocked_by
    target: /initiatives/INIT-014-multi-repository-workspaces/epics/EPIC-004-multi-repo-run-lifecycle/tasks/TASK-002-create-run-branches-across-repositories.md
---

# TASK-003 - Clean Up Partial Branch Creation Failures

---

## Purpose

Avoid orphaning run branches when startup fails halfway through a multi-repo branch creation sequence.

## Deliverable

Add cleanup logic to delete already-created local and remote branches if later repository branch creation fails.

## Acceptance Criteria

- Cleanup runs for every repo whose branch was already created.
- Cleanup errors are logged without hiding the original startup failure.
- Remote cleanup runs when auto-push created remote branches.
- The run record is not persisted if startup fails before activation.
- Tests cover failure on first, middle, and last repository.

