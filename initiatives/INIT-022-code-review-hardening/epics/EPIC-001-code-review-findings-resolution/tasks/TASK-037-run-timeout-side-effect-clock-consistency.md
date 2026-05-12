---
id: TASK-037
type: Task
title: "Thread injected clock into run-timeout side-effect timestamps"
status: Pending
acceptance: Pending
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-12
last_updated: 2026-05-12
links:
  - type: parent
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-028-harness-advance-clock.md
  - type: related_to
    target: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/tasks/TASK-016-scenario-run-timeout.md
---

# TASK-037 — Thread injected clock into run-timeout side-effect timestamps

---

## Purpose

`internal/scheduler/run_timeout.go:19-20` reads `s.now()` only to
drive the `ListTimedOutRuns` predicate. The side effects that follow —
`UpdateRunStatus` (writes `completed_at` via the store's `now()`) and
`EmitLogged` (fills a zero event timestamp via `time.Now()`) — still
read the wall clock. In an `Advance(2h)` scenario the injected clock
is past `timeout_at` while the persisted cancellation timestamp and
emitted event timestamp remain in real-time before that deadline.
Result: a run is cancelled-for-timeout *before* its own recorded
`completed_at` reaches the timeout. Time-based scenario assertions and
audit-log invariants both break.

This is a P2 correctness finding from the 2026-05-12 codex review of
the INIT-022 batch (commit `49efb3adf6`). It is the most substantive
finding from that pass.

## Deliverable

- Thread the scan-time `now` produced by `s.now()` in
  `ScanRunTimeouts` into the timeout-handling code path so:
  - `UpdateRunStatus` records `completed_at = scan_now` (not the
    store's `now()`).
  - `EmitLogged` receives `event_ts = scan_now` (not `time.Now()`).
- This implies a small surface-level change to the store
  `UpdateRunStatus` family or a dedicated `UpdateRunStatusAt(now)`
  variant — pick the smaller delta. Document the choice in the
  resolution.
- Audit `internal/scheduler/orphan.go` and `internal/scheduler/timeout.go`
  for the same shape; if the side effects there also bypass `s.now()`,
  fix them in the same PR.

## Acceptance Criteria

- A new scenario advances the harness clock past `timeout_at`, scans,
  and observes:
  - `run.completed_at == advanced_now` (within the scan-tick window).
  - The emitted timeout event's `event_ts == advanced_now`.
- Reverting any one of the three reads (predicate / status update /
  event emit) to wall time fails the scenario.
- Production behaviour unchanged: when `s.now == time.Now`, all three
  reads still land within microseconds of each other as today.

## Out of Scope

- A global "scheduler-side `time.Now` audit" — only the run-timeout
  family. Other paths (engine, divergence, etc.) keep wall time until
  a separate finding motivates a change.
- Replacing the store's `now()` Postgres-side default with a clock
  parameter on every method.
