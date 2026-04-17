---
id: TASK-005
type: Task
title: "Consolidate Create/Update into writeAndCommit and fix artifact.Update rollback"
status: Pending
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-005 — Consolidate writeAndCommit + Fix Update Rollback

---

## Purpose

`internal/artifact/service.go` L127-203 (Create) vs L232-309 (Update) and `internal/workflow/service.go` L111-161 vs L166-224 share a 10-step skeleton: parse → validate → enterBranch → safePath → stat check → write file → stage+commit → rollback on error → autoPush → emitEvent. Only the existence check (exists vs not-exists), commit-message verb, and event type differ. Beyond the dedup, Create rolls back both the file and the git commit on failure, but `artifact.Update` at L293 only rewrites the original without a matching `gitReset` — a partial-failure in Update can leave the working tree dirty.

Depends on TASK-004 (shared `EnterBranch`/`StageAndCommit` must land first).

---

## Deliverable

1. Add a private `writeAndCommit(ctx, path, content string, op writeOp) (*WriteResult, error)` on each service, where `writeOp` encodes:
   - Pre-check (exists / not-exists).
   - Commit-message verb.
   - Event type.
   - Rollback policy (full rollback for both Create and Update).
2. Replace Create/Update bodies with thin wrappers that pick the right `writeOp`.
3. Fix the artifact.Update rollback asymmetry: on write/commit failure, reset the git worktree in addition to restoring the file.
4. Add a regression test that Update leaves neither the file nor the git state dirty when the commit step fails.

---

## Acceptance Criteria

- Each service has exactly one write-and-commit implementation.
- artifact.Update rollback matches artifact.Create (file + git reset).
- New test verifies Update partial-failure cleanup.
- Existing artifact/workflow tests pass unchanged.
