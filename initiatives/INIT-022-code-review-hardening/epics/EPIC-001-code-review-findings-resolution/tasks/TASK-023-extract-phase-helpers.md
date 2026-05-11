---
id: TASK-023
type: Task
title: "Extract phase helpers from MergeRunBranch and checkrunner.Run"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-11
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

## Resolution (2026-05-11)

`internal/engine/merge.go`: `MergeRunBranch` (199 LOC at review time)
split into a flat orchestrator plus four phase helpers. The
orchestrator body is 13 LOC (well under the ~80 cap) and reads
top-down as `preflightCheck → mergeCodeRepos → mergePrimary →
verifyAndComplete`.

| Helper | Total LOC | Code-only LOC | Responsibility |
| --- | ---: | ---: | --- |
| `preflightCheck` | 46 | 27 | GetRun + state check, empty-branch fast path, branch-protection, applyCommitStatus. |
| `mergeCodeRepos` | 15 | 10 | Per-repo merge loop wrapped with the `errPendingCodeRepoRetry` → retryMerge route. |
| `mergePrimary` | 100 | 63 | Primary merge + push, with the retry / renumber / permanent-fail classification for both git errors. |
| `verifyAndComplete` | 24 | 16 | EPIC-005 AC #5 completeness check; transitionToPartiallyMerged vs completeAfterMerge + advancePublishStepIfAny. |

`internal/checkrunner/local_command.go`: `Run` (~190 LOC) split into
the slim Run shell (11 LOC) plus three lifecycle helpers, supported
by two small bundle structs (`commandPrep`, `captureResult`) that
shuttle state between phases.

| Helper | Total LOC | Code-only LOC | Responsibility |
| --- | ---: | ---: | --- |
| `prepareCommand` | 69 | 48 | Validate kind/command/working_dir, resolve absolute path, stat, open log sink. |
| `runAndCapture` | 113 | 52 | Timeout-bounded sub-context, command/cancel/process-group wiring, `cmd.Run`, process-group sweep, post-run snapshots. |
| `classify` | 34 | 19 | Finalise `Result` — LogReference, sink-write short-circuit, log writer Close, classifyExit, close-error surfacing. |

**Signature pattern: `(done bool, err error)`.** Both `mergeCodeRepos`
and `mergePrimary` return a leading `done` flag (truncated to
`retried` for code-repos) that the caller checks alongside `err`:

```go
if done, err := o.mergePrimary(ctx, run); err != nil || done {
    return err
}
```

`done == true` means the helper has fully handled the run's state
transition (retry track, permanent-fail track, …) and the caller
should not advance further. `preflightCheck` uses the same pattern
to signal the empty-branch fast path. This keeps the four return
paths from the original — recErr persistence failure, transient
retry, permanent fail, success — visible at the orchestrator level
without leaking a sentinel error.

**Mechanical only.** Every original return path maps 1:1 to a path
in the new helpers (verified by diff inspection: the diff is a
union of doc-comment additions and `return …` → `return X, …`
shape changes; no statement was added, removed, or reordered).
`classify` reads `runCtxErr` / `parentCtxErr` / `leaderExitCode`
from `captureResult` snapshots taken inside `runAndCapture` after
`cmd.Run` returned — preserving the original ordering rationale
(codex pass 2 P2: snapshot before slow logWriter.Close so a slow
close cannot reflip context state). The `defer cancel()` for the
runner-imposed timeout now fires when `runAndCapture` returns
rather than when `Run` returns — both are post-`cmd.Run` and any
context-watch goroutine has already done its work, so the
observable behaviour is identical.

**LOC deviation from the AC's ~80 line cap.** `mergePrimary` (100
total) and `runAndCapture` (113 total) exceed the cap when the
preserved load-bearing comments are counted. Statement-only counts
(blank lines and `//`-comment lines stripped) are 63 and 52 — both
comfortably under 80. The inline comments are not decorative: each
one documents a specific codex-pass-derived invariant (e.g.
`captureResult.runCtxErr` snapshot rationale, the
`runnerKilledLeader` Windows note, the cmd.WaitDelay leader-exit
override) that must travel with the moved code under the
mechanical-only constraint. Splitting `mergePrimary` further into
`handleMergeError` + `handlePushError` is the natural further
seam, but doing so would deviate from the spec's enumerated
four-helper structure; preserving the spec's structure and
documenting the LOC overflow under load-bearing comments is the
better trade.

**Test gates**

- `go build ./...` — clean.
- `go vet ./internal/engine/... ./internal/checkrunner/...` — clean.
- `gofmt -l internal/engine/merge.go internal/checkrunner/local_command.go` — clean.
- `go test ./internal/engine/... ./internal/checkrunner/... -count=1 -race` — green.
- `go test ./... -count=1 -race` — green except the pre-existing
  `internal/secrets/file_test.go::TestFileClient_VersionChangesOnEdit`
  flake (TASK-026 territory, not a refactor regression).
- `go test -tags=scenario -count=1 ./internal/scenariotest/scenarios/...`
  — green.
- `make docker-lint` — 206 baseline unchanged. Initial rewrite
  introduced two `builtinShadow` findings (`cap` shadowing the
  builtin) on `local_command.go:159` and `:402`; renamed the
  local + parameter to `capture`, restoring the baseline.
- `codex review` — clean: "The refactor preserves the original
  return paths and ordering for the merge and local command runner
  flows. The noted lifecycle details around commit-status failure
  handling, log writer close ordering, context snapshots, timeout
  cancel lifetime, and done/error propagation appear equivalent."
