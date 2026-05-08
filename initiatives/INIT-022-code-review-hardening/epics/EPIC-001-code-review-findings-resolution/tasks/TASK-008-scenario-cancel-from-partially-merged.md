---
id: TASK-008
type: Task
title: "Scenario: cancel from partially-merged"
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

# TASK-008 — Scenario: cancel from partially-merged

---

## Purpose

The cancel exit from `partially-merged` is asymmetric: succeeded-repo
branches are deleted by `CleanupRunBranch`, failed-repo branches are
preserved for operator inspection. This contract is documented in
`architecture/multi-repository-integration.md §4.5` and
`architecture/error-handling-and-recovery.md §5.4` but has **no
scenario guard**.

This is a P1 coverage finding from the 2026-05-07 code review.

## Deliverable

A scenario (new file or new `Test*` in TASK-006's file) that:

1. Reaches `partially-merged` with at least one merged repo and one
   failed repo.
2. Calls `run.cancel`.
3. Asserts:
   - The run lands at `cancelled`.
   - The merged repo's branch is **deleted** (not present on the local
     git tree under that repo).
   - The failed repo's branch is **preserved** (still present on the
     local git tree under that repo).
   - The primary Spine repo's run branch follows the contract
     documented in §4.5 (whatever it says — read the doc, encode the
     assertion).

## Acceptance Criteria

- Scenario passes deterministically.
- Asymmetric cleanup is asserted explicitly per repo (not "all gone"
  / "none gone").
- Removing the failed-branch-preservation conditional from
  `CleanupRunBranch` makes the scenario fail deterministically.

## Out of Scope

- The retry exit — TASK-006.
- The external-resolution exit — TASK-007.
- Run-timeout cancellation — TASK-016.

## Resolution (2026-05-08)

**Scenario:** `internal/scenariotest/scenarios/cancel_from_partially_merged_test.go`
(`TestCancelFromPartiallyMerged_AsymmetricCleanup`). One workspace +
two code repos (`billing`, `shipping`, both wired via
`harness.WithCodeRepos`). Two code repos are required for the AC: a
merged + a failed side-by-side is the only configuration that proves
the cleanup decision is per-repo, not run-wide. Namespaced under
init-905 / epic-905 so it does not collide with TASK-006 (init-903) or
TASK-007 (init-904) in the same package.

The scenario drives a multi-repo run through:

1. StartRun → SubmitStepResult("completed") → SubmitStepResult("accepted").
2. The `accepted` outcome carries `commit: status: Completed`, so
   `IngestResult` transitions the run to `committing` and immediately
   fires `MergeRunBranch`.
3. A pre-queued `git.GitError{Kind: ErrKindPermanent, Message: "merge
   conflict"}` on billing fires once. Shipping's wrapped client falls
   through to the underlying CLI and merges normally. Primary merges.
   `firstPermanentCodeRepoFailure` finds billing →
   `transitionToPartiallyMerged` runs.
4. `assertCancelPartiallyMergedShape` pins the triple: run
   `partially-merged`, primary `merged` (non-empty MergeCommitSHA),
   shipping `merged` (non-empty MergeCommitSHA), billing `failed`
   with `FailureClass=merge_conflict`.
5. `assertAllBranchesPreservedWhileParked` pins the
   `multi-repository-integration.md §4.5` invariant: while in
   `partially-merged`, every affected repo's run branch stays on disk.
   All three trees (billing, shipping, primary) are checked.
6. `cancelRunFromPartiallyMerged` invokes `Orchestrator.CancelRun`
   with a synthesised operator actor in ctx (`domain.WithActor`).
   `CancelRun` flips run status to `cancelled` and invokes
   `CleanupRunBranch` internally.
7. `assertCancelRunStatusCancelled` pins the run-state side of the
   exit: per `engine-state-machine.md §2.2`, partially-merged →
   cancelled is the operator-cancel transition.
8. `assertAsymmetricBranchCleanup` pins the load-bearing AC:
   - billing (failed) → `assertLocalBranchExists` — branch preserved
     so the operator can resolve against the unmodified ref.
   - shipping (merged) → `assertBranchAbsentAt` — branch deleted.
   - primary (merged) → `assertBranchAbsentAt` — branch deleted (per
     §4.5 the primary follows the same per-outcome rule as any code
     repo; it is not exempt from cleanup just because it is primary).

**Why two code repos (and not the single-repo shape TASK-006/007 use):**
the AC explicitly requires asymmetric cleanup ("not 'all gone' / 'none
gone'"). One merged + one failed side-by-side is the only configuration
that produces a per-repo decision the cleanup pass must honour. With a
single code repo, `CleanupRunBranch` would either keep everything (the
single repo failed) or delete everything (the single repo merged) and
the AC's asymmetry claim could not be exercised.

**Helper reuse:** `assertBranchAbsentAt` was already defined in
`multi_repo_run_lifecycle_test.go` in the same `scenarios_test`
package, with a careful exit-code discriminator (only `git rev-parse`
exit 1 = clean ref-not-found counts as absence; exit 128 = repo
missing, ENOENT, etc. propagate as errors). The scenario reuses it
rather than re-implementing the absence check, after a codex review
flagged a first-cut helper that treated all `cmd.Run()` errors as
absence (which would let a corrupted-tempdir teardown masquerade as
successful cleanup).

**Regression-bait verification:** I removed the
`if outcome.Status == domain.RepositoryMergeStatusFailed { preserved[outcome.RepositoryID] = true }`
conditional from `Orchestrator.preservedRepoBranches` in
`internal/engine/branch.go`, re-ran the scenario, and confirmed
`assert-asymmetric-branch-cleanup` failed with "billing (failed)
branch must be preserved by cleanup: branch ... missing in <dir>".
Restoring the case restored the pass — the assertion is load-bearing,
not boilerplate. Matches the AC's explicit regression-bait clause.

**Codex iterative review:** P3 finding on first pass (the home-rolled
`assertLocalBranchAbsent` could false-pass on non-absence git errors);
fixed by switching to the existing `assertBranchAbsentAt` helper.
Two consecutive clean passes after the fix.

**Test gates:**

- `go test ./...` (unit) — green except `TestFileClient_VersionChangesOnEdit`,
  the same pre-existing `internal/secrets` flake reproducible on bare
  main and out of scope for TASK-008 (already documented in
  TASK-006/TASK-007 resolutions).
- `go test -tags scenario -p 1 -run 'TestCancelFromPartiallyMerged_AsymmetricCleanup|TestPartialMergeRetry_HappyPath|TestPartialMergeExternalResolution_HappyPath|TestMultiRepoRunLifecycle' ./internal/scenariotest/...`
  — all four green.
- `golangci-lint run ./...` — default-scope count unchanged; my new
  file (gated by `//go:build scenario`) contributes zero findings even
  when re-run with `--build-tags scenario`.
