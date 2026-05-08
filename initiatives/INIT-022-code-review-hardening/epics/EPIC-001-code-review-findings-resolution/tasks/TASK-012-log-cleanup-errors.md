---
id: TASK-012
type: Task
title: "Log + propagate cleanup errors in engine.run and divergence.service"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-08
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-012 — Log + propagate cleanup errors in engine.run and divergence.service

---

## Purpose

Three call sites in `internal/engine/run.go` (`:422`, `:462`, `:488`)
and one in `internal/divergence/service.go:231` swallow cleanup errors
silently with `_ = …Cleanup…(...)`. A failed cleanup leaves a branch
alive while the surrounding logs say "run completed/failed/cancelled"
or "branch deleted". Over time this leaks orphan branches.

This is a P2 error-handling finding from the 2026-05-07 code review.

## Deliverable

- At each of the four sites, replace the blank-assign with at minimum a
  `slog.Warn` log line carrying:
  - Operation name (`cleanup_run_branch`, `delete_branch`).
  - The relevant ID (`run_id`, `branch`).
  - The error.
- For the engine.run sites, evaluate whether the calling state-machine
  transition should observe the error (e.g. record a `cleanup_failed`
  event) — if so, do that.
- For divergence.service, the rollback is best-effort by design;
  Warn-level log is sufficient.

## Acceptance Criteria

- All four sites have explicit logging on cleanup failure.
- A unit test in `internal/engine` confirms a forced cleanup error
  produces the documented log shape (using the test logger pattern
  already in the package).
- `git grep "_ = .*Cleanup\|_ = .*DeleteBranch" internal/` returns
  only intentional cases, each with a comment.

## Out of Scope

- A repo-wide audit of every `_ =` blank-assign. This task is the
  four flagged sites.

## Resolution (2026-05-08)

Replaced each blank-assign with an explicit `if err := …; err != nil`
that logs a `slog.Warn` at the call site:

- `internal/engine/run.go` (CompleteRun-ish completion path,
  `FailRun`, `CancelRun`) — three sites. `CompleteRun` reuses the
  function-scoped `log`; `FailRun` and `CancelRun` introduce a local
  `log := observe.Logger(ctx)` so the existing Info line and the new
  Warn line share a single logger.
- `internal/engine/merge.go` (post-merge cleanup) — one fifth site
  not in the original ticket but flagged by the AC's `git grep`
  audit. Same pattern.
- `internal/divergence/service.go` (CreateBranch DB-failure
  rollback) — one site. Best-effort by design (a failed rollback
  cannot reasonably be propagated when the caller is already
  receiving the underlying store error), so a Warn-level log is
  sufficient.

State-machine transitions do *not* observe the cleanup error — the
internal `recordCleanupFailure` path inside `CleanupRunBranch`
already emits `EventRunBranchCleanupFailed` for every per-repo
failure, so a parallel call-site emission would duplicate. The
call-site log adds the missing signal: when the *primary* delete
fails (the only condition under which `CleanupRunBranch` returns
non-nil), an operator now sees the failure attached to the
originating run-completion / -failed / -cancelled / -merged line.

Files:

- `internal/engine/run.go` — three call-site rewrites.
- `internal/engine/merge.go` — one call-site rewrite.
- `internal/divergence/service.go` — one call-site rewrite.
- `internal/engine/cleanup_log_test.go` — new test file with
  `captureSlog` helper, `errorDeleteGit` stub, and
  `TestFailRun_CleanupErrorLogged` /
  `TestCancelRun_CleanupErrorLogged` asserting the warn shape.
- `internal/engine/branch_cleanup_test.go` — added a comment to the
  one remaining `_ = h.orch.CleanupRunBranch(...)` test-side
  discard so the AC grep audit returns only commented-intentional
  cases.

Test gates:

- `go test ./internal/engine/... ./internal/divergence/... -race`:
  green; new tests pass.
- Full unit suite: green (the secrets `TestFileClient_VersionChangesOnEdit`
  flake is nondeterministic and was clear on this run).
- `golangci-lint run ./...`: 206 issues — same baseline as TASK-011.
- `gosec ./...`: 91 issues — same baseline as `main` (the prior 88
  figure was a stale measurement; verified with `git stash` test
  this run).
- `codex review --uncommitted`: two consecutive clean passes.
