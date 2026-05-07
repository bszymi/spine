---
id: TASK-004
type: Task
title: "Add internal/git/branchwrite_test.go"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-004 — Add internal/git/branchwrite_test.go

---

## Purpose

`internal/git/branchwrite.go` has no dedicated `_test.go`. `EnterBranch`
(`:53-97`) implements a documented TOCTOU mitigation (parent-dir +
fresh child) and a cleanup that uses a fresh bounded context to survive
request cancellation (`:86-94`). `StageAndCommit` (`:103-124`) handles
trailer ordering and rollback-on-failure (`:120`). The only existing
mention is one indirect reference in `internal/artifact/policy_test.go:211`.

Filesystem-race regressions and leaked-worktree regressions would not
be caught today.

This is a P1 coverage finding from the 2026-05-07 code review.

## Deliverable

Add `internal/git/branchwrite_test.go` driving a real Git CLI through
`NewTestRepo` (the existing pattern used by `cli_test.go`):

- **EnterBranch happy path**: enter a fresh branch, confirm the
  worktree exists at the expected path with the expected HEAD.
- **EnterBranch on existing branch**: enter a previously-created branch,
  confirm reuse semantics (whatever the documented behavior is).
- **EnterBranch cancellation cleanup**: cancel the parent context
  mid-operation, assert the cleanup uses its own bounded context and
  the parent-dir / child-dir cleanup actually completes (`:86-94`).
- **StageAndCommit happy path**: stage a file, commit, confirm trailer
  ordering matches the documented format (governance commit format).
- **StageAndCommit rollback**: simulate a commit failure (e.g. by
  passing an invalid signing flag or by removing the index between
  stage and commit), assert the rollback at `:120` actually runs.

## Acceptance Criteria

- `go test ./internal/git -run TestBranchWrite` passes; depends only
  on the standard `git` binary on PATH (same as existing
  `cli_test.go`).
- All four code paths flagged above have at least one test.
- The cancellation-cleanup test deterministically demonstrates the
  fresh-context behavior — i.e. would fail if someone removed the
  `context.WithoutCancel`/equivalent from `:86-94`.

## Out of Scope

- Tests for `internal/git/cli.go` Diff/MergeBase argv hardening — that
  is TASK-009.
- Refactoring `branchwrite.go` itself. If a refactor turns out to be
  needed to make tests deterministic, it ships in this PR but stays
  minimal.
