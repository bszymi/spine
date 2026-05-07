---
id: TASK-028
type: Task
title: "Build harness.AdvanceClock primitive"
status: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-07
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-016-scenario-run-timeout.md
---

# TASK-028 — Build harness.AdvanceClock primitive

---

## Purpose

Time-based scheduler paths (`Scheduler.handleRunTimeout`, orphan
detection, partial-merge retry sweep) are currently testable only at
unit level with fakes. The scenario harness has no
`AdvanceClock`/`ScheduleTick` primitive, so end-to-end coverage of
these paths is structurally impossible.

This is a P3 harness affordance finding from the 2026-05-07 code
review.

## Deliverable

- Introduce a `Clock` abstraction at the harness boundary (or thread
  through an existing one if one already exists in `internal/`).
- Add `harness.AdvanceClock(d time.Duration)` that:
  - Bumps the harness clock.
  - Triggers any clock-driven schedulers that should fire as a
    consequence (or makes the next manual scheduler tick observe the
    new time).
- Migrate `multi_repo_run_lifecycle_test.go` (or a representative
  scenario that has a `time.Sleep` today) to use the primitive as a
  validator.

## Acceptance Criteria

- A new scenario can advance the harness clock and observe the
  scheduler tick acting on the new time.
- TASK-016 (run-timeout scenario) is rewritten or extended to use the
  primitive.
- Existing scenarios that don't touch time are unaffected.

## Out of Scope

- Replacing `time.Now()` calls across production code with the
  Clock — only at the seams the scheduler reads.
- Production-side determinism beyond the test seam.
