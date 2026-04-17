---
id: TASK-004
type: Task
title: "Extract shared git branch-write plumbing used by artifact and workflow services"
status: Completed
epic: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
initiative: /initiatives/INIT-008-dogfooding-fixes/initiative.md
work_type: refactor
created: 2026-04-17
last_updated: 2026-04-17
completed: 2026-04-17
links:
  - type: parent
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-005-code-quality-2026-04/epic.md
---

# TASK-004 — Extract Shared Git Branch-Write Plumbing

---

## Purpose

`internal/artifact/service.go` L33-41, L362-444 and `internal/workflow/service.go` L55-107, L366+ each declare a package-local `branchScope` struct and implement `enterBranch`, `stageAndCommit`, plus `gitAdd`, `gitCommitPath`, `gitReset`, `gitCurrentBranch`, `autoPush`. The workflow file carries a literal comment: *"mirroring internal/artifact.Service.enterBranch"*. Any bug found on one side has to be fixed twice.

---

## Deliverable

1. Create `internal/git/branchwrite.go` with:
   - `type WriteScope struct { RepoDir string; Branch string; Cleanup func() }`
   - `func EnterBranch(ctx context.Context, client Client, repo, branch string) (*WriteScope, error)`
   - `func StageAndCommit(ctx context.Context, scope *WriteScope, path string, opts git.CommitOpts) (git.CommitResult, error)` — reuses the existing `git.CommitOpts { Message, Author, Trailers }` and `git.CommitResult { SHA }` shapes so artifact.Service and workflow.Service keep populating `WriteResult.CommitSHA` and emitting trace/actor/run/operation trailers unchanged. Preserve the fixed trailer ordering (`Trace-ID`, `Actor-ID`, `Run-ID`, `Operation`, `Workflow-Bypass`).
   - `func ResetWorktree(ctx context.Context, scope *WriteScope) error`
2. Move `autoPush` logic alongside so both services share one push implementation. Keep env-var gating (`SPINE_GIT_AUTO_PUSH`) unchanged.
3. Remove the per-service `branchScope`, `enterBranch`, `stageAndCommit`, `gitAdd`, `gitCommitPath`, `gitReset`, `gitCurrentBranch`, `autoPush`. Services keep their domain logic (validation, path safety, event emission) but delegate all git mechanics.
4. Preserve the existing `GIT_LITERAL_PATHSPECS=1` env for git add/commit/reset.

---

## Acceptance Criteria

- `internal/git/branchwrite.go` is the single implementation of the write-scope lifecycle.
- `internal/artifact/service.go` and `internal/workflow/service.go` no longer contain `branchScope` or duplicated git-cli calls.
- Artifact and workflow integration tests pass unchanged.
- Scenario tests pass.
