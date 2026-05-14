---
id: TASK-037
type: Task
title: "Thread injected clock into run-timeout side-effect timestamps"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: bugfix
created: 2026-05-12
last_updated: 2026-05-14
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

## Resolution (2026-05-14)

The scheduler's run-timeout scan now captures `scanNow := s.now()`
once at scan entry and threads that value into all three downstream
side effects so an injected clock is observed coherently. A new
`RunStore.UpdateRunStatusAt(ctx, runID, status, completedAt)` method
provides the seam — the existing `UpdateRunStatus` keeps its
Postgres-side `now()` default for the engine / gateway / recovery
paths that don't run under an injected clock, preserving production
behaviour bit-for-bit when `s.now == time.Now`.

**Surface decision**: a dedicated `UpdateRunStatusAt` variant rather
than a `time.Time` parameter on `UpdateRunStatus`. The latter would
force every caller (engine `run.go` × 7, gateway, recovery, the `Tx`
interface) to either thread a clock or pass `time.Time{}` with a
"zero means SQL `now()`" convention — both invasive and easy to
silently get wrong. The new method is only called from places where
the scheduler's clock seam matters (one call site today). The `Tx`
interface in `store.go` is unchanged: `run_timeout.go` does not run
inside a transaction, so a Tx variant would have no caller.

**Files touched**

- `internal/store/interfaces.go` — `RunStore.UpdateRunStatusAt`
  declared with the seam rationale inline (why this exists, what
  fails without it).
- `internal/store/postgres_runs.go` — `(*PostgresStore).UpdateRunStatusAt`
  parallels `UpdateRunStatus` byte-for-byte except `completed_at`
  uses `$3` instead of `now()`. `started_at` still uses Postgres
  `now()` — the scheduler never transitions a run *into* `active`,
  so seeding `started_at` from the scan clock would be premature
  generalisation.
- `internal/scheduler/run_timeout.go` — `ScanRunTimeouts` captures
  `scanNow := s.now()`, passes it through to a new fourth parameter
  on `handleRunTimeout`, which calls `UpdateRunStatusAt(ctx, runID,
  Cancelled, scanNow)` and sets `Timestamp: scanNow` on the emitted
  `EventRunTimeout` so `EmitLogged`'s zero-Timestamp fallback never
  fires for this path. Docstring on `ScanRunTimeouts` documents the
  three-read coherence contract and the "cancelled before its own
  recorded completed_at" inconsistency it closes.
- `internal/scheduler/scheduler_test.go` — `fakeStore.UpdateRunStatusAt`
  override that mirrors `UpdateRunStatus` and additionally writes
  the supplied `completedAt` into the in-memory run row so future
  unit tests can assert on it. Without this override the embedded
  nil `store.Store` would panic when the scheduler's run-timeout
  path is exercised.
- `cmd/spine/stubstore_test.go`, `internal/gateway/stubstore_test.go`
  — `(stubRunStore).UpdateRunStatusAt` no-op added so the compile-
  time `var _ store.RunStore = stubRunStore{}` assertions keep
  passing after the interface gained a method.
- `internal/scenariotest/scenarios/run_timeout_scenario_test.go` —
  new scenario `TestRunTimeout_CancellationTimestampsTrackInjectedClock`
  alongside the existing `Scanner...` scenario, plus two new helper
  steps `assertRunCompletedAtEqualsClock` and
  `assertRunTimeoutEventTimestampEqualsClock`. The scenario reuses
  the existing harness wiring (workflow YAML, `startRunNamed`,
  `registerRunTimeoutScannerOnClock`, `advanceHarnessClock`,
  `recordingEventRouter`) so the only new surface is the two
  assertion helpers. `completed_at` is compared at microsecond
  precision (Postgres truncates `timestamptz`); event `Timestamp`
  is compared at full nanosecond precision (no DB round-trip).

**Audit of `orphan.go` and `timeout.go`** (per spec bullet):

- `internal/scheduler/timeout.go` (step-timeout, not run-timeout):
  already clock-consistent before this PR. `s.now()` at the
  predicate (line 51), `now := s.now()` at line 75 used for both
  `exec.CompletedAt = &now` (which `UpdateStepExecution` then
  persists from the struct) and the emitted event's
  `Timestamp: now`. No changes needed.
- `internal/scheduler/orphan.go`: clock-consistent for the
  scheduler's own reads (predicate + 3×-threshold branch), but the
  side effect calls the externally-injected `runFailFn(ctx, runID,
  reason)` whose signature has no time argument and whose target
  (`orch.FailRun` in `internal/engine`) owns its own clock. Widening
  `RunFailFunc` would propagate through `internal/workspace/pool.go`,
  `cmd/spine/serve_*.go`, and the engine package — a wider seam
  change than this finding warrants. Documented as out-of-scope; the
  engine's run-fail clock is a separate concern that would justify
  its own task if it ever becomes load-bearing on a scenario.

**Acceptance criteria satisfied**

- *A new scenario advances the harness clock past `timeout_at`,
  scans, and observes `run.completed_at == advanced_now` and
  emitted event's `event_ts == advanced_now`.* ✓ —
  `TestRunTimeout_CancellationTimestampsTrackInjectedClock` does
  exactly this. Both assertions pass on `-count=3` runs.
- *Reverting any one of the three reads (predicate / status update /
  event emit) to wall time fails the scenario.* ✓ — Three bait-
  checks verified manually before checking in:
  - Predicate revert (`s.now()` → `time.Now()`): scenario fails at
    `assert-run-status-clocked_run-cancelled` because the wall
    clock has not crossed `timeout_at`, the predicate returns no
    rows, no run is cancelled.
  - Status-update revert (`UpdateRunStatusAt` → `UpdateRunStatus`):
    fails at `assert-completed-at-equals-clock-clocked_run` with
    `diff -1h59m59...` — Postgres wrote `completed_at` from
    transaction-time `now()` while the harness clock advanced 2h.
  - Event-emit revert (drop `Timestamp: scanNow`): fails at
    `assert-event-ts-equals-clock-clocked_run` with the same ~2h
    diff — `EmitLogged`'s zero-Timestamp fallback wrote
    `time.Now()` instead of the scan clock.
  All three reverts restored immediately after the bait check.
- *Production behaviour unchanged: when `s.now == time.Now`, all
  three reads still land within microseconds of each other as
  today.* ✓ — `New` defaults `now` to `time.Now`; in production,
  `scanNow` and Postgres' `now()` differ only by the wire round-
  trip latency. `UpdateRunStatus` (Postgres `now()`) is unchanged
  and remains the path engine / gateway / recovery use. The new
  `UpdateRunStatusAt` is only invoked by `ScanRunTimeouts`, and
  only that one call site reads from `s.now()` instead of the DB.

**Codex review iteration**

First-pass codex review clean on the first submission: *"No
correctness issues were found in the reviewed changes. The new
timestamp threading is consistently applied through the scheduler,
store method, and event emission paths."* No follow-up needed.

**Test gates**

- `go build ./...` / `go vet ./...` / `go vet -tags scenario ./...`
  — clean.
- `go test -count=1 ./internal/scheduler/... ./internal/store/...
  ./internal/gateway/... ./internal/engine/... ./cmd/spine/...
  ./internal/event/... ./internal/delivery/...` — green (the unit
  suites that own the touched interfaces and consumers).
- `go test -tags scenario -run 'TestRunTimeout' -count=3
  ./internal/scenariotest/scenarios/...` — both run-timeout
  scenarios (the existing predicate-isolation one and the new
  timestamp one) pass on every iteration.
- `make docker-lint` — 207 issues, identical to baseline at commit
  `f0c3c4a` (no new findings on touched files).
- `codex review --uncommitted` — clean on first submission.
