---
id: TASK-004
type: Task
title: Git Commit Retry Mechanism
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-004-git-orchestration/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-004-git-orchestration/epic.md
---

# TASK-004 — Git Commit Retry Mechanism

## Purpose

Implement automatic retry for runs stuck in the `committing` state due to transient Git failures.

## Deliverable

- Scheduler detects runs in `committing` state beyond a threshold
- Retry logic re-attempts the merge operation with backoff
- Maximum retry count before transitioning to `failed`
- Logging and metrics for commit retry attempts

## Acceptance Criteria

- Runs stuck in `committing` for more than the threshold are retried
- Retries use exponential backoff
- After maximum retries, the run transitions to `failed` with error detail
- Each retry attempt is logged with retry count
- Idempotent: re-running the merge on an already-merged branch is a no-op
