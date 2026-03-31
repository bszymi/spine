---
id: TASK-002
type: Task
title: Push after artifact writes
status: Completed
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

# TASK-002 — Push After Artifact Writes

---

## Purpose

After every artifact create or update that produces a Git commit, push the current branch to origin.

---

## Deliverable

`internal/artifact/service.go`

After the commit in `Create()` and `Update()`:
- Call `git.Push(ctx, "origin", currentBranch)`
- Log push errors as warnings — do not fail the artifact operation on push failure
- Respect a configuration flag to disable auto-push

Add `SPINE_GIT_AUTO_PUSH` environment variable (default: `true`). When `false`, skip push.

---

## Acceptance Criteria

- Artifact create on main pushes to origin
- Artifact create on a planning run branch pushes to origin
- Push failure is logged but does not fail the create/update operation
- Auto-push can be disabled via `SPINE_GIT_AUTO_PUSH=false`
