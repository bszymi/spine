---
id: TASK-006
type: Task
title: "Scenario: partial-merge retry happy path"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-08
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: blocked_by
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-005-harness-with-code-repos-helper.md
---

# TASK-006 — Scenario: partial-merge retry happy path

---

## Purpose

INIT-014's partial-merge recovery shipped with unit tests in
`internal/engine/merge_resolution_test.go` only. No scenario exercises
`Orchestrator.RetryRepositoryMerge` end-to-end through the scheduler's
`retryCommittingRuns` gate. The architecture doc
(`error-handling-and-recovery.md §5.4`) promises a two-step model
(operator clears outcome, scheduler resumes); without a scenario,
nothing fails if either side regresses.

This is a P1 coverage finding from the 2026-05-07 code review.

## Deliverable

A new scenario file
`internal/scenariotest/scenarios/partial_merge_retry_test.go` (or a new
`Test*` function in a sibling) that:

1. Uses `harness.WithCodeRepos` (TASK-005) to set up a workspace with
   primary + one code repo.
2. Drives a multi-repo run to `committing` state.
3. Configures the code repo's stub behavior to fail the merge with a
   `failed` outcome (conflict class).
4. Asserts the run lands at `partially-merged` with the expected
   `merge_outcomes` shape.
5. Calls `Orchestrator.RetryRepositoryMerge` (or the equivalent gateway
   route).
6. Configures the code repo's behavior to succeed on retry.
7. Runs a scheduler tick (or relies on the harness's existing tick
   plumbing).
8. Asserts the run reaches `completed` with `merge_outcomes` showing
   `merged` for the previously-failed repo.

## Acceptance Criteria

- Scenario passes against a clean DB and clean repo tree.
- The scenario's transitions match the contract documented in
  `architecture/error-handling-and-recovery.md §5.4` and
  `architecture/engine-state-machine.md §2.2`.
- Removing the gate keying in `scheduler.retryCommittingRuns` against
  `codeRepoOutcomesAllowResume` makes the scenario fail
  deterministically (regression bait check during PR development).

## Out of Scope

- Testing the `ResolveRepositoryMergeExternally` exit — that is
  TASK-007.
- Testing the cancel-from-partially-merged exit — that is TASK-008.

## Resolution (2026-05-08)

**Scenario:** `internal/scenariotest/scenarios/partial_merge_retry_test.go`
(`TestPartialMergeRetry_HappyPath`). One workspace + one code repo
(`billing`, wired via `harness.WithCodeRepos`). The scenario drives a
multi-repo run through:

1. StartRun → SubmitStepResult("completed") → SubmitStepResult("accepted").
2. The `accepted` outcome carries `commit: status: Completed`, so
   `IngestResult` transitions the run to `committing` and immediately
   fires `MergeRunBranch` (engine/run.go's "immediate merge" comment).
3. A pre-queued `git.GitError{Kind: ErrKindPermanent, Message: "merge
   conflict"}` on billing's wrapped client triggers the per-repo
   failure → primary merges → `transitionToPartiallyMerged` runs.
4. `assertPartiallyMergedShape` pins run.Status, primary outcome
   merged with non-empty MergeCommitSHA, billing outcome failed with
   FailureClass=`merge_conflict`.
5. `assertGatedTickIsNoOp` (the regression-bait check called out in
   the AC) constructs a real `scheduler.Scheduler` wired with
   `orch.MergeRunBranch` as `commitRetryFn`, runs `RunRetryCycle`,
   and asserts the primary outcome's Attempts counter has not grown.
   I verified the bait fires by temporarily disabling the
   `codeRepoOutcomesAllowResume` gate (`if status == ... && false`)
   — the test failed with "primary attempts after gated tick: got 2,
   want 1".
6. `RetryRepositoryMerge` is invoked with a synthesised operator
   actor in ctx (`domain.WithActor`). The recovery result is asserted
   to have `ReadyToResume=true` and empty `BlockingRepositories`; the
   billing outcome row is now `pending`.
7. A second `RunRetryCycle` opens the gate, transitions
   partially-merged → committing, MergeRunBranch re-attempts the
   billing merge (queue empty now → falls through to the underlying
   CLI client), succeeds, primary merge re-records, run completes.
8. `assertRunCompletedWithMergedOutcomes` pins both outcomes at
   `merged` with non-empty MergeCommitSHAs.

**Why the Attempts assertion (not run.Status) for the regression-bait:**
without the gate, the scheduler transitions partially-merged →
committing, MergeRunBranch fires, billing's outcome is already
terminal-failed so the per-repo loop short-circuits, primary merge
runs (recordPrimaryMergeOutcome bumps Attempts), then
`transitionToPartiallyMerged` puts the run right back. End-state
status is partially-merged either way; only the inflated Attempts
counter is observable.

**Codex iterative review:** two consecutive clean passes (`codex
review --uncommitted`); no findings raised.

**Test gates:**

- `go test ./...` (unit) — green
- `go test -tags scenario ./internal/scenariotest/...` — my new
  scenario passes; pre-existing parallel-cleanup failures in the
  broader scenario suite reproduce identically without my change
  (verified by stashing partial_merge_retry_test.go and re-running)
  and are out of scope for TASK-006.
- `golangci-lint run ./...` — repo-wide count holds at 206
  pre-existing issues; my new file contributes zero.
