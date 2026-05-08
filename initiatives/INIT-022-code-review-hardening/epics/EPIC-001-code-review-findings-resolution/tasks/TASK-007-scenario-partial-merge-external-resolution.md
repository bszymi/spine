---
id: TASK-007
type: Task
title: "Scenario: partial-merge external resolution"
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

# TASK-007 — Scenario: partial-merge external resolution

---

## Purpose

The external-resolution exit from `partially-merged`
(`Orchestrator.ResolveRepositoryMergeExternally`) lets an operator
record a target SHA when the merge happened outside Spine. It produces
a ledger commit on the per-run audit branch and exits the run. Only
unit-test coverage exists; no scenario asserts the ledger-commit shape
or the run's exit semantics.

This is a P1 coverage finding from the 2026-05-07 code review.

## Deliverable

A scenario (new file or new `Test*` in TASK-006's file) that:

1. Reaches `partially-merged` via the same setup as TASK-006 (one
   failed merge outcome).
2. Calls `Orchestrator.ResolveRepositoryMergeExternally(runID, repoID,
   targetSHA)` with an externally-merged SHA.
3. Asserts:
   - The repo's outcome flips to `resolved-externally`.
   - The run advances to `completed`.
   - A ledger commit lands on the per-run audit branch with the
     expected actor, message shape, and target SHA reference.
   - The original failed branch is preserved (per the §4.4 cleanup
     contract).

## Acceptance Criteria

- Scenario passes deterministically.
- Ledger-commit assertions cite the documented commit format from
  `architecture/git-integration.md §5`.
- The exit-state transitions match
  `architecture/error-handling-and-recovery.md §5.4`.

## Out of Scope

- Testing the retry exit — TASK-006.
- Testing the cancel exit — TASK-008.

## Resolution (2026-05-08)

**Scenario:** `internal/scenariotest/scenarios/partial_merge_external_resolution_test.go`
(`TestPartialMergeExternalResolution_HappyPath`). One workspace + one
code repo (`billing`, wired via `harness.WithCodeRepos`) — same
single-repo shape TASK-006 uses; multi-failed-repo blocking-list
permutations stay scoped to the unit test
`TestMergeRecovery_BlockingRepositoriesSurfacedWhenMultipleFailed`
and to TASK-008. The scenario drives a multi-repo run through:

1. StartRun → SubmitStepResult("completed") → SubmitStepResult("accepted").
2. The `accepted` outcome carries `commit: status: Completed`, so
   `IngestResult` transitions the run to `committing` and immediately
   fires `MergeRunBranch`.
3. A pre-queued `git.GitError{Kind: ErrKindPermanent, Message: "merge
   conflict"}` on billing's wrapped client triggers the per-repo
   failure → primary merges → `transitionToPartiallyMerged` runs.
4. `assertExternalResolutionPartiallyMergedShape` pins run.Status,
   primary outcome merged with non-empty MergeCommitSHA, billing
   outcome failed with FailureClass=`merge_conflict`.
5. `assertFailedBranchPreservedWhileParked` pins the
   `multi-repository-integration.md §4.5` invariant: while in
   `partially-merged`, both billing's run branch and the primary's
   run branch stay on disk so the operator can inspect the failed
   branch alongside the merged work.
6. `ResolveRepositoryMergeExternally` is invoked with a synthesised
   operator actor in ctx (`domain.WithActor`) and a target SHA. The
   recovery result is asserted to have `ReadyToResume=true`,
   `BlockingRepositories` empty, and `LedgerCommitSHA` non-empty;
   the billing outcome row flips to `resolved-externally` with
   `ResolvedBy` / `ResolutionReason` set and failure metadata
   cleared.
7. A scheduler `RunRetryCycle` opens the gate
   (`codeRepoOutcomesAllowResume` ignores `resolved-externally`),
   transitions partially-merged → committing, MergeRunBranch
   re-walks. The terminal-skip guard in
   `internal/engine/multi_repo_merge.go` skips billing (status is
   already terminal), the primary re-merges, and the run advances
   to `completed`.
8. `assertExternalResolutionRunCompleted` pins both outcomes:
   primary `merged` with non-empty MergeCommitSHA, billing still
   `resolved-externally` with the operator's audit pair intact (a
   regression that re-merged billing on the resume tick would
   overwrite `ResolvedBy` / `ResolutionReason` and fail here).
9. `assertLedgerCommitOnMain` greps `git log main` for the ledger
   commit by SHA and asserts the trailers documented in
   `architecture/git-integration.md §5` (Run-ID, Trace-ID,
   Operation) plus the resolve-specific extras (Repository-ID,
   Resolved-By, Target-Commit-SHA), and that the operator reason
   lives in the body — not in a trailer (the trailer-injection guard).

**Why two assertions on billing's audit pair (post-resolve and
post-completion):** the post-resolve check confirms the orchestrator
wrote the row correctly; the post-completion check confirms the
scheduler resume + second `MergeRunBranch` did NOT overwrite it. This
is the production contract the unit-level
`TestMergeRecovery_PartialMergeRetriedAfterResolveExternally` pins
against fakes; the scenario re-pins it against real Postgres + on-disk
repos.

**Regression-bait verification:** I removed
`RepositoryMergeStatusResolvedExternally` from
`domain.RepositoryMergeStatus.IsTerminal`, re-ran the scenario, and
confirmed `assert-run-completed` failed with "billing outcome after
resume: got merged, want resolved-externally (terminal-skip must
preserve the audit pair)". Restoring the case restored the pass — the
assertion is load-bearing, not boilerplate.

**Codex iterative review:** two consecutive clean passes (`codex
review --uncommitted`); no findings raised.

**Test gates:**

- `go test ./...` (unit) — green except for `TestFileClient_VersionChangesOnEdit`,
  a pre-existing `internal/secrets` flake reproducible on bare main
  (verified by re-running against the parent commit) and out of scope
  for TASK-007.
- `go test -tags scenario -p 1 -run 'TestPartialMergeExternalResolution_HappyPath|TestPartialMergeRetry_HappyPath|TestMultiRepoRunLifecycle|TestCrossRepoEvidence' ./internal/scenariotest/...` — green.
- `golangci-lint run ./...` — repo-wide count holds at 206
  pre-existing issues; my new file (gated by `//go:build scenario`)
  contributes zero findings even when re-run with `--build-tags scenario`.
