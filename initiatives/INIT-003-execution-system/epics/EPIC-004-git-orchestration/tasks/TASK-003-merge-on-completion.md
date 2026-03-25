---
id: TASK-003
type: Task
title: Merge Strategy and Run Completion
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-004-git-orchestration/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-004-git-orchestration/epic.md
---

# TASK-003 — Merge Strategy and Run Completion

## Purpose

Implement the Spine-owned merge from run branch to the authoritative branch when a run completes successfully.

## Deliverable

- Merge logic triggered on run completion (committing → completed transition)
- Spine-owned merge: the system performs the merge, not the actor
- Merge conflict detection and reporting
- Run transitions to `failed` if merge fails (with conflict detail)
- Merge commit includes structured trailers (Run-ID, Trace-ID)

## Acceptance Criteria

- Successful run completion merges the run branch to the authoritative branch
- Merge conflicts are detected and the run fails with clear error detail
- The merge commit includes all required trailers
- Failed runs do not merge (branch is preserved for debugging)
- Cancelled runs do not merge (branch is cleaned up)
