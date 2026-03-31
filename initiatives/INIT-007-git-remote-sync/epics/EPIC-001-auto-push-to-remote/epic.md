---
id: EPIC-001
type: Epic
title: Auto-Push to Remote
status: Pending
initiative: /initiatives/INIT-007-git-remote-sync/initiative.md
owner: bszymi
created: 2026-03-31
last_updated: 2026-03-31
links:
  - type: parent
    target: /initiatives/INIT-007-git-remote-sync/initiative.md
---

# EPIC-001 — Auto-Push to Remote

---

## 1. Purpose

Make Spine automatically push all Git changes to origin so collaborators can see branches, review artifacts, and pull work without manual intervention.

The workspace setup flow is: user creates a Git repo on their hosting platform (GitHub, Bitbucket, GitLab, etc.), clones it locally, and points Spine at it. From that point, every Spine Git operation should be reflected on origin.

---

## 2. Scope

### In Scope

- Push after every commit (artifact create, artifact update, run completion merge)
- Push after branch creation (planning runs, standard runs)
- Push branch deletion after run cleanup
- Generic implementation using `git push origin <ref>` — no provider-specific code
- Error handling: transient push failures should be retried, permanent failures surfaced
- Configuration option to disable auto-push (for offline/local-only usage)
- Update `internal/git/cli.go` with push operations
- Update engine and artifact service to call push after git operations
- Scenario tests for push behavior

### Out of Scope

- Git credential management
- Webhook or pull-based sync
- Force push (never needed — Spine owns its branches)

---

## 3. Success Criteria

1. After `artifact.Create()` or `artifact.Update()`, the commit is pushed to origin
2. After `StartRun()` or `StartPlanningRun()`, the branch is pushed to origin
3. After `MergeRunBranch()`, the merge commit is pushed and the branch is deleted on origin
4. After `CancelRun()` cleanup, the branch is deleted on origin
5. Push failures are logged and surfaced but do not block the local operation
6. Auto-push can be disabled via configuration

---

## 4. Key Files

- `internal/git/cli.go` — add `Push()`, `DeleteRemoteBranch()` methods
- `internal/artifact/service.go` — call push after commit
- `internal/engine/run.go` — call push after branch creation
- `internal/engine/merge.go` — call push after merge, delete remote branch after cleanup

---

## 5. Related Artifacts

- `/architecture/git-integration.md`
- `/initiatives/INIT-006-governed-artifact-creation/initiative.md`
