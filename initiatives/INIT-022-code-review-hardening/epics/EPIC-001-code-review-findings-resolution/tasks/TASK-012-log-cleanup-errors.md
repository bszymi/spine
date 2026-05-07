---
id: TASK-012
type: Task
title: "Log + propagate cleanup errors in engine.run and divergence.service"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-07
last_updated: 2026-05-07
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
