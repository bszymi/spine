---
id: TASK-032
type: Task
title: "Assert Actor-ID trailer in partial-merge external resolution scenario"
status: Pending
acceptance: Pending
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
