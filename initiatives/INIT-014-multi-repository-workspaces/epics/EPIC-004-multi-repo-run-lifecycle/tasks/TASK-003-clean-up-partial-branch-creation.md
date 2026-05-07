---
id: TASK-003
type: Task
title: Clean up partial branch creation failures
status: Completed
acceptance: Approved
acceptance_rationale: |
  Partial-fan-out cleanup shipped on main: when StartRun's per-repo
  branch creation fails after some repos have already received their
  branches, the orchestrator deletes the branches it created on the
  way in so the run does not leave operators with dangling
  spine/run/* refs in arbitrary repos. The cleanup mirrors the
  per-repo outcome semantics later refined in EPIC-005 §4.5.
  Acceptance was missing from frontmatter when the task closed;
  backfilled here as part of the post-INIT-014 status coherence
  sweep.
last_updated: 2026-05-07
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

