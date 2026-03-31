---
id: TASK-001
type: Task
title: Add push operations to Git client
status: Done
epic: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/epic.md
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
work_type: implementation
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/epics/EPIC-001-auto-push-to-remote/epic.md
---

# TASK-001 — Add Push Operations to Git Client

---

## Purpose

Add `Push()` and `DeleteRemoteBranch()` methods to the Git CLI client so Spine can sync changes with origin.

---

## Deliverable

`internal/git/cli.go`

Add:
- `Push(ctx, remote, ref string) error` — pushes a ref to the remote. Uses `git push <remote> <ref>`.
- `PushBranch(ctx, remote, branch string) error` — pushes a branch with tracking. Uses `git push -u <remote> <branch>`.
- `DeleteRemoteBranch(ctx, remote, branch string) error` — deletes a branch on the remote. Uses `git push <remote> --delete <branch>`.

All methods should:
- Use the existing `run()` helper for command execution
- Classify push errors (auth failure, network, rejected) via `classifyGitError()`
- Not use `--force` — Spine owns its branches, force push is never needed

---

## Acceptance Criteria

- Three new methods on `CLIClient`
- Push errors are classified (transient vs permanent)
- No force push
- Unit tests with mock git operations
