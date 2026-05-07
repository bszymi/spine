---
id: TASK-007
type: Task
title: "Scenario: partial-merge external resolution"
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
