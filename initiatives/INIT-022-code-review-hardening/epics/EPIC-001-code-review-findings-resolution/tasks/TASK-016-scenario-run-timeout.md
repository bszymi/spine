---
id: TASK-016
type: Task
title: "Scenario: run-timeout cancellation"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-028-harness-advance-clock.md
---

# TASK-016 — Scenario: run-timeout cancellation

---

## Purpose

`Scheduler.handleRunTimeout` only has unit-test coverage in
`scheduler/run_timeout_test.go` against a fake store. No scenario
exercises `ListTimedOutRuns` against real Postgres + run-cancellation
event emission.

This is a P2 coverage finding from the 2026-05-07 code review.

## Deliverable

A scenario in `internal/scenariotest/scenarios/`:

1. Seed a run with `started_at` aged past `SPINE_RUN_TIMEOUT` — either
   by directly stamping the row, or via the `harness.AdvanceClock`
   primitive once TASK-028 lands.
2. Run a scheduler tick.
3. Assert:
   - The run reaches `cancelled`.
   - A `run_timeout` event was emitted.
   - The associated run branch is cleaned up per the documented
     timeout-cancellation contract.

## Acceptance Criteria

- Scenario passes deterministically.
- Removing the `ListTimedOutRuns` predicate keying on `started_at`
  causes the scenario to fail.
- Compatible with TASK-028's clock primitive — if TASK-028 lands
  first, use it; otherwise stamp the row directly and document why.

## Out of Scope

- Per-step timeouts (separate state-machine concern).
- Workflow-level timeouts.
