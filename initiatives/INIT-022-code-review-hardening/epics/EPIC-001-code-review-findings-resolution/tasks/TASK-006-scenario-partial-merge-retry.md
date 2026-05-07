---
id: TASK-006
type: Task
title: "Scenario: partial-merge retry happy path"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
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
