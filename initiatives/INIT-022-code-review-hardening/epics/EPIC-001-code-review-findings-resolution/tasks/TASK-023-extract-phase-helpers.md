---
id: TASK-023
type: Task
title: "Extract phase helpers from MergeRunBranch and checkrunner.Run"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
---

# TASK-023 — Extract phase helpers from MergeRunBranch and checkrunner.Run

---

## Purpose

Two long functions sit at the top of the engine + checkrunner
readability ceiling:

- `internal/engine/merge.go:28 MergeRunBranch` — 198 lines mixing
  pre-flight checks, code-repo loop, primary merge, push, and
  per-failure classification.
- `internal/checkrunner/local_command.go:200 Run` — 204 lines with
  deep error-classification branching.

This is a P3 maintainability finding from the 2026-05-07 code review.

## Deliverable

- For `MergeRunBranch`, extract:
  - `preflightCheck(...)` — anything before the code-repo loop.
  - `mergeCodeRepos(...)` — the per-repo loop body.
  - `mergePrimary(...)` — primary merge + push.
  - `verifyAndComplete(...)` — post-merge verification + completion
    transition.
- For `checkrunner.Run`, extract:
  - `prepareCommand(...)`
  - `runAndCapture(...)`
  - `classify(...)` — the ctx-error vs exec-error distinction.
- No behavior changes; existing tests must pass unchanged.

## Acceptance Criteria

- All existing tests in `internal/engine` and `internal/checkrunner`
  pass.
- Each extracted helper is under ~80 lines.
- The original `MergeRunBranch` and `Run` shells are under ~80 lines
  each, focused on orchestration.

## Out of Scope

- Behavior changes — purely structural.
- Tests for the new helpers (they remain covered through the
  outer functions).
