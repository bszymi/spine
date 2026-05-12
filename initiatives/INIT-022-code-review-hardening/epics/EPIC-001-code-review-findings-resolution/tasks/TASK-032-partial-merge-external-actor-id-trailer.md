---
id: TASK-032
type: Task
title: "Assert Actor-ID trailer in partial-merge external resolution scenario"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-007-scenario-partial-merge-external-resolution.md
---

# TASK-032 — Assert Actor-ID trailer in partial-merge external resolution scenario

---

## Purpose

`internal/scenariotest/scenarios/partial_merge_external_resolution_test.go:526-532`
pins the ledger commit format documented in
`architecture/git-integration.md §5`, but only asserts the resolve-
specific `Resolved-By` trailer. As written, a regression that produces
a ledger commit with `Resolved-By` but omits the standard `Actor-ID`
trailer still passes — so the scenario does not catch a violation of
the documented commit shape and actor requirement it is meant to lock
down.

This is a P2 scenario-coverage finding from the 2026-05-12 codex
review of the INIT-022 batch (commit `8923838cc7`).

## Deliverable

- Extend the existing trailer assertion block to also require
  `Actor-ID: <expected>` on the external-resolution ledger commit
  exactly as the rest of the engine writes it (use the same expected-
  value source the scenario already uses for `Resolved-By`, not a
  hardcoded literal).
- If `architecture/git-integration.md §5` enumerates additional
  required trailers not currently covered (e.g., `Spine-Run-ID`),
  fold them in the same PR — the goal is "the scenario pins the
  documented commit shape", not "one extra line".

## Acceptance Criteria

- Reverting the `Actor-ID` trailer emission in the engine path fails
  this scenario with a clear `trailer Actor-ID missing` style diff.
- Existing happy-path assertions still pass.

## Out of Scope

- The shape of the ledger commit itself — this is a test-only
  reinforcement of an existing documented contract.
- Pinning trailers on the non-external resolution path (TASK-006 /
  TASK-008 cover their respective scenarios; if those have the same
  gap, file separately).

## Resolution (2026-05-12)

Surveying `writeMergeRecoveryLedgerCommit` revealed that the engine
ledger commit path was building its trailer map by hand and was
missing the §5-required `Actor-ID` trailer entirely. The "existing
documented contract" pointed at in the task description (architecture/
git-integration.md §5.1) was therefore not actually being honored on
this code path — only the operation-specific synonyms (`Resolved-By` /
`Requested-By`) were emitted. The fix is dual: emit `Actor-ID` on the
engine side, then pin it at both the unit and scenario layers.

**Files touched**

- `internal/engine/merge_resolution.go` — `writeMergeRecoveryLedgerCommit`
  now sets `trailers["Actor-ID"] = action.ActorID` alongside Trace-ID /
  Run-ID / Operation. Comment block expanded to explain that
  `Resolved-By`/`Requested-By` are operation-specific synonyms and do
  not substitute for the §5-canonical Actor-ID trailer.
- `internal/engine/merge_resolution_test.go` — both
  `TestResolveRepositoryMergeExternally_WritesLedgerCommit` and
  `TestRetryRepositoryMerge_WritesLedgerCommit` now assert
  `Trailers["Actor-ID"] == fix.actorID`, citing §5.1 in the failure
  message so a regressing diff points at the architecture contract,
  not just the test.
- `internal/scenariotest/scenarios/partial_merge_external_resolution_test.go`
  — `wantTrailers` in `assertLedgerCommitOnMain` gained
  `"Actor-ID": externalResolutionActorID`, with a comment that names
  the §5 contract and explicitly calls out the
  "skip Actor-ID, keep Resolved-By" regression class this assertion
  catches.

**Acceptance criteria satisfied**

- *Reverting the `Actor-ID` trailer emission in the engine path fails
  this scenario with a clear `trailer Actor-ID missing` style diff.*
  ✓ — Verified via `git stash` of just `merge_resolution.go`:
  `step "assert-ledger-commit-on-main" failed: ledger trailer
  Actor-ID: got "", want "actor-op-external-1"`. Engine restored
  immediately after the bait-check.
- *Existing happy-path assertions still pass.* ✓ — Full
  `TestPartialMergeExternalResolution_HappyPath` green;
  multi-repo / cross-repo / partial-merge family
  (`TestPartialMerge|TestMultiRepo|TestCrossRepo|TestRepositoryDeactivate|TestCancelFromPartiallyMerged|TestPrimaryRepoInTaskRepositories`)
  green in one batch.

**Trailers beyond Actor-ID (per the deliverable's §5 sweep)**

The §5 required-trailer set is Trace-ID, Actor-ID, Run-ID, Operation.
The ledger commit already emits Trace-ID, Run-ID, Operation
(verified at `merge_resolution.go:536-541`) and now emits Actor-ID.
`Run-ID` is set to the actual run ID rather than `none` because the
ledger commit is part of a run; that matches §5.1's "Conditional —
Run ID if the commit is part of workflow execution". No further §5
trailers needed; the engine-specific trailers
(`Repository-ID`, `Resolved-By`/`Requested-By`, `Target-Commit-SHA`)
remain extras beyond §5 and are unchanged.

**Test gates**

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./internal/engine/... ./internal/git/... ./internal/observe/... -count=1` — green.
- `go test -tags scenario -run 'TestPartialMerge|TestMultiRepo|TestCrossRepo|TestRepositoryDeactivate|TestCancelFromPartiallyMerged|TestPrimaryRepoInTaskRepositories' ./internal/scenariotest/scenarios/... -count=1` — green.
- `make docker-lint` — 207 issues, identical to baseline at commit
  `f0c3c4a` (no new findings on touched files).
- `codex review --uncommitted` — clean: *"No actionable correctness
  issues were found in the current changes. The added Actor-ID
  trailer is consistent with the existing ledger commit flow and
  test updates."*
