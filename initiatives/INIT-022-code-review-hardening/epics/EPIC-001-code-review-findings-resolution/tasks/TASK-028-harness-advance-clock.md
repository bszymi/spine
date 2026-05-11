---
id: TASK-028
type: Task
title: "Build harness.AdvanceClock primitive"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: refactor
created: 2026-05-07
last_updated: 2026-05-11
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

## Resolution (2026-05-11)

Three changes wire a deterministic clock seam into the scheduler and a
controllable clock primitive into the scenario harness:

1. **`internal/scheduler/`** — added `Scheduler.now func() time.Time`
   (defaults to `time.Now`) and exported `WithClock(now func() time.Time)`
   in `options.go`. The scan-loop policy reads in `run_timeout.go`,
   `timeout.go`, and `orphan.go` now route through `s.now()`. Recovery
   paths intentionally stay on `time.Now` directly — they write
   `CreatedAt` timestamps and event-ID nonces rather than gating
   policy on a comparison, so injecting a clock there would only add a
   seam tests don't need. `orphan.go` now reads the clock once per
   tick and reuses it for both the threshold cutoff and the 3×-stale
   check, replacing one `time.Now() + time.Since` pair with one
   `s.now() + now.Sub`; within a tick this is a tiny consistency win.

2. **`internal/scenariotest/harness/clock.go`** — new `Clock` type with
   `NewClock(t0)`, `Now()`, `Advance(d)`, and `OnAdvance(name, fn)`.
   `Advance` bumps the clock under lock then fires handlers
   sequentially with the lock released so handlers may re-enter
   `Now`. The first handler error is returned and wrapped with the
   handler name; subsequent handlers still run so the failing
   assertion sees every observable side effect. A unit test
   (`clock_test.go`) pins ordering, error wrapping, and race-safety.

3. **`internal/scenariotest/harness/runtime.go`** — added
   `TestRuntime.Clock *Clock`, seeded to `NewClock(time.Now())` in
   `NewTestRuntime`. The wall-clock seed keeps the harness clock
   comparable to timestamps that production code (e.g.,
   `engine.StartRun`) writes via real `time.Now`, so the engine
   doesn't need to opt into the clock for the scheduler-side seam to
   work.

The TASK-016 scenario `run_timeout_scenario_test.go` is rewritten to
use the new primitive. The workflow's timeout is shortened from `24h`
to `1h` so the natural `Run.TimeoutAt = realNow + 1h` sits inside the
2-hour advance window. `fresh_run`'s `timeout_at` is then stamped to
`realNow + 168h` via `Store.ExecRaw` — that keeps the predicate
isolation test (`AND timeout_at <= $1` mutation target) alive across
the advance. The old helper `stampRunTimeoutAtPast` is replaced by
`stampRunTimeoutAt(stateKey, offset)` (now stamping forward, not
back), and the old `runScanRunTimeoutsWithRecorder` synchronous
scheduler call is replaced by `registerRunTimeoutScannerOnClock`
(constructs the scheduler with `WithClock(clock.Now)`, registers
`ScanRunTimeouts` as an `OnAdvance` handler) plus a
`advanceHarnessClock(2h)` step.

**Acceptance criteria satisfied:**

- *A new scenario can advance the harness clock and observe the
  scheduler tick acting on the new time.* ✓ — The rewritten scenario
  drives both halves: the harness clock advance is what the scheduler
  observes via `ListTimedOutRuns(s.now())`, and the registered
  `OnAdvance` handler fires the scan synchronously.
- *TASK-016 (run-timeout scenario) is rewritten or extended to use the
  primitive.* ✓ — Rewritten (see above).
- *Existing scenarios that don't touch time are unaffected.* ✓ —
  `TestRuntime.Clock` is populated unconditionally but only observed
  by callers that pass `Clock.Now` into a scheduler; the full
  `make test` + scenario tag suite confirms no regressions.

**Regression-bait verification** (manual, pre-submission):

| Mutation | Result |
| --- | --- |
| Revert `ScanRunTimeouts` to `now := time.Now()` (bypass injected clock) | FAIL — `TestRunTimeout_ScannerCancelsExpiredRunAndPreservesFresh/assert-run-status-expired_run-cancelled`: `run run-<id> status: got active, want cancelled`. The harness advance is no longer observed; the scheduler reads real wall clock, sees `timeout_at = realNow + 1h` as still-in-future, and skips the run. |

**Test gates**

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l internal/scheduler/ internal/scenariotest/harness/ internal/scenariotest/scenarios/run_timeout_scenario_test.go` — clean on edited files (pre-existing baseline drift in `recovery_partial_test.go` and `multirepo_internal_test.go` unrelated, last touched by TASK-005 commit `cb9f7b2`).
- `go test ./... -count=1 -race` — green (38 packages).
- `go test -tags=scenario -count=1 -run TestRunTimeout ./internal/scenariotest/scenarios/...` — green; full scenario suite runs match main baseline (unrelated `validation_failed` failures pre-date this PR).
- `make docker-lint` — 206 baseline unchanged.
- `golangci-lint --enable-only=gosec ./internal/scheduler/... ./internal/scenariotest/...` — 0 issues.
- `codex review` — clean: *"No actionable correctness issues were found in the clock injection, harness clock locking pattern, or the updated run-timeout scenario coverage."*
