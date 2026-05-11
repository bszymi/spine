---
id: TASK-024
type: Task
title: "Delete dead code in workflow.binding and divergence.convergence"
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

# TASK-024 — Delete dead code in workflow.binding and divergence.convergence

---

## Purpose

Two dead-code sites flagged by the 2026-05-07 review:

- `internal/workflow/binding.go:65` — `_ = specificCandidates` blank
  assigns a non-trivial computed value with a "TODO when structured
  applies_to is implemented" comment.
- `internal/divergence/convergence.go:226` — `_ = evalRecord //
  stored in event payload` is a misleading no-op; the value is
  already used in the payload built two lines above.

This is a P3 cleanup finding from the 2026-05-07 code review.

## Deliverable

- Delete the `specificCandidates` computation entirely. If the TODO
  is genuinely warranted, file a follow-up task; do not leave the
  computed-then-discarded value in tree.
- Delete the `_ = evalRecord` line and its comment.
- Run `git grep "_ = "` across `internal/` and confirm any other hits
  are intentional (they typically are; only flag a finding if the
  blank obscures real logic).

## Acceptance Criteria

- `go build ./...` passes.
- Existing tests pass without modification.
- The two flagged blank-assigns are gone.

## Out of Scope

- A repo-wide blank-assign sweep.

## Resolution (2026-05-11)

**`internal/workflow/binding.go`.** Removed `var specificCandidates`
(never assigned) and the dead `if workType != "" { … _ =
specificCandidates … candidates = generalCandidates }` block that
guarded only the redundant reassignment. `generalCandidates` is now
just `candidates` — there is nothing to contrast "general" against
in the v0.x code path. The function's `workType` parameter is still
load-bearing: it flows into the `ErrWorkflowNotFound` and
`ErrConflict` error messages so operator-facing failures stay
specific about which (type, work_type, mode) tuple had no / multiple
matches. A short comment captures *why* work_type filtering is
absent (structured `applies_to` not yet implemented) so a future
implementer has the context without the dead variable in tree.

**`internal/divergence/convergence.go`.** Removed
`evalRecord, _ := json.Marshal(output.Result)` and the trailing
`_ = evalRecord // stored in event payload` blank assign. The
comment was wrong: `evalRecord` (the full marshalled `output.Result`)
is **not** in the event payload — the payload is built two lines
later from the specific fields `divergence_id`, `strategy`,
`selected_branch`, `selected_branches`. The marshal call was
discarded work.

**Blank-assign audit.** `git grep "_ = "` across `internal/` returns
~145 hits. The non-test sites that look unusual at a glance are all
documented intentional uses:

- `internal/checkrunner/local_command.go` and `internal/engine/merge.go`
  — best-effort cleanup (process-group cancel, log writer Close on
  short-circuit, `git checkout main` rollback after staged edits).
- `internal/delivery/webhook_dispatcher.go` — store-update best-effort
  paths under the dispatcher's "fail forward" contract.
- `internal/gateway/handlers_artifacts.go` and `handlers_tasks.go` —
  `decodeJSON` ignored when body is documented optional.
- `internal/gateway/handlers_events_stream.go:199` — `_ = err` after a
  filtered SetWriteDeadline error; the inline comment explains the
  fallback (heartbeat loop as weaker liveness guard).
- `internal/cli/output.go`, `internal/config/config.go` — defer
  Close / Fprintln to a tab writer where the error would only
  surface on a downstream consumer issue.

Test-file hits dominate the count and are all intentional discards.
No other site obscures real logic.

**Test gates**

- `go build ./...` — clean.
- `go vet ./internal/workflow/... ./internal/divergence/...` — clean.
- `gofmt -l` — clean.
- `go test ./internal/workflow/... ./internal/divergence/... -count=1 -race`
  — green.
- `go test ./... -count=1 -race` — green except the pre-existing
  `internal/secrets/file_test.go::TestFileClient_VersionChangesOnEdit`
  flake (TASK-026 territory, not a refactor regression).
- `go test -tags=scenario` — green.
- `make docker-lint` — 206 baseline unchanged.
- `codex review` — see commit message / PR for the quoted pass.
