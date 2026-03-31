---
id: TASK-003
type: Task
title: Push after branch create, merge, and delete
status: Done
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/epic.md
  - type: blocked_by
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/tasks/TASK-001-git-push-operations.md
---

# TASK-003 — Push After Branch Create, Merge, and Delete

---

## Purpose

Push branch lifecycle events to origin so collaborators can see, pull, and work on Spine branches.

---

## Deliverable

### 1. Branch creation push

`internal/engine/run.go` — in `StartRun()` and `StartPlanningRun()`, after `git.CreateBranch()`:
- Call `git.PushBranch(ctx, "origin", branchName)`
- Log push errors as warnings

### 2. Merge push

`internal/engine/merge.go` — in `MergeRunBranch()`, after successful merge:
- Push the authoritative branch: `git.Push(ctx, "origin", authoritativeBranch)`
- Only proceed to branch cleanup if the main push succeeds (see §3)

### 3. Branch cleanup push

`internal/engine/branch.go` — in `CleanupRunBranch()`, after local branch deletion:
- Delete the remote branch: `git.DeleteRemoteBranch(ctx, "origin", branchName)`
- Log errors as warnings — remote branch may already be gone

**Critical ordering:** Remote branch deletion must only happen after the authoritative branch push succeeds. If pushing main to origin fails, the remote run branch is the only remote ref containing the merged commits. Deleting it would make the work unreachable for collaborators. If the main push fails, skip remote branch deletion and let the scheduler retry the push on the next cycle.

All operations respect `SPINE_GIT_AUTO_PUSH` configuration.

---

## Acceptance Criteria

- Planning run branch appears on origin immediately after creation
- After merge to main, main is pushed to origin
- After branch cleanup, branch is deleted on origin — only if the main push succeeded
- Remote branch deletion is skipped when the authoritative branch push fails
- All push errors are logged but non-fatal
