---
id: TASK-002
type: Task
title: "Run lifecycle edge case scenarios"
status: Completed
epic: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
initiative: /initiatives/INIT-004-product-scenario-testing/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-004-product-scenario-testing/epics/EPIC-008-scenario-coverage-gaps/epic.md
---

# TASK-002 — Run lifecycle edge case scenarios

---

## Purpose

The existing run lifecycle scenarios only exercise the happy path. Several failure modes and edge cases are untested: run timeout expiry, duplicate result submission on a completed step (idempotency under concurrent pressure), and attempting an invalid state transition (e.g. submitting a result for a cancelled run).

## Deliverable

Scenario tests covering:

- **Run timeout**: create a run with a very short `Timeout` value, advance time or trigger the timeout check, verify the run moves to `timed_out` status and its branch is cleaned up
- **Submit to cancelled run**: start a run, cancel it, then attempt to submit a step result — expect a domain error (not a panic or silent no-op)
- **Duplicate result submission**: submit a result for a step, then submit again with the same execution ID — verify the second submission is a no-op (idempotent) and does not double-advance the workflow
- **Start run on already-running task**: call `StartRun` for a task that already has an active run — verify the second call returns an appropriate error rather than creating a second run

## Acceptance Criteria

- Timeout scenario: run status becomes `timed_out` and branch is deleted
- Submit-to-cancelled: returns a domain error with code indicating the run is no longer active
- Duplicate submission: second call returns success without side effects (step count unchanged)
- Double-start: second `StartRun` returns an error; only one run exists for the task
