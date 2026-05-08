---
id: TASK-016
type: Task
title: "Scenario: run-timeout cancellation"
status: Completed
acceptance: Approved
epic: /initiatives/INIT-022-code-review-hardening/epics/EPIC-001-code-review-findings-resolution/epic.md
initiative: /initiatives/INIT-022-code-review-hardening/initiative.md
work_type: test
created: 2026-05-07
last_updated: 2026-05-08
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

## Resolution (2026-05-08)

Added `internal/scenariotest/scenarios/run_timeout_scenario_test.go`,
which exercises `Scheduler.ScanRunTimeouts` against the test
PostgreSQL instance and the real event-router surface. The shape of
the scenario:

1. Seed a single-step workflow with a workflow-level `timeout: "24h"`
   so `StartRun` populates `runtime.runs.timeout_at` via the same
   path production uses.
2. Seed two task hierarchies (INIT-916 / INIT-917) and start one run
   for each, capturing both run IDs and branch names under separate
   state slots (`expired_run_id`, `fresh_run_id`).
3. Stamp the expired run's `timeout_at` to `now − 1h` via
   `Store.ExecRaw` — the existing test-only escape hatch already in
   `internal/store/testutil.go`. TASK-028 (harness clock primitive)
   is still pending; AC bullet 3 explicitly accepts the direct stamp
   as an alternative and asks for it to be documented, which the
   scenario file does inline.
4. Construct a `scheduler.Scheduler` using the runtime store and a
   purpose-built `recordingEventRouter` (satisfies
   `event.EventRouter`, captures every emitted event under a mutex)
   so the post-tick assertions are synchronous and never race the
   `MemoryQueue`'s background dispatch.
5. Call `sched.ScanRunTimeouts(ctx)` once.
6. Assert four invariants:
   - The expired run's status flipped to `cancelled`.
   - The fresh run's status is still `active`.
   - A `run_timeout` event was emitted carrying the expired run's
     RunID.
   - No `run_timeout` event was emitted for the fresh run.
   - The expired run's branch is still on disk in the test repo.

The fresh-run assertion is the AC bullet 2 mutation-test target.
Verified locally by removing `AND timeout_at <= $1` from
`PostgresStore.ListTimedOutRuns` and re-running the scenario: the
assertion `assert-run-status-fresh_run-active` fails with
`run <id> status: got cancelled, want active`. Restoring the
predicate makes the scenario pass again. (TASK-016's purpose section
references `started_at`, but the implementation in
`internal/store/postgres_runs.go` keys on `timeout_at <= $1`; the
mutation target tracks the implementation that ships today.)

The branch-preservation assertion encodes the documented intentional
gap from `architecture/multi-repository-integration.md §4.5`:
"Scheduler-driven run timeouts (`Scheduler.handleRunTimeout`) flip a
run to cancelled but do not call `CleanupRunBranch`." A regression
that quietly added `CleanupRunBranch` to `handleRunTimeout` without
updating §4.5 would surface here as a deleted branch and fail the
step. If a future change *intentionally* wires cleanup into the
timeout path, both this scenario and §4.5 must change in lockstep —
the scenario file's doc-comment calls this out so future readers
know to update both.

Files:

- `internal/scenariotest/scenarios/run_timeout_scenario_test.go` —
  new scenario file under the `//go:build scenario` tag. Contains
  the workflow YAML constant, the `TestRunTimeout_…` test, four
  scenario-step helpers (`startRunNamed`, `stampRunTimeoutAtPast`,
  `runScanRunTimeoutsWithRecorder`, plus three asserters), the
  `recordingEventRouter` fake, and a self-contained
  `assertLocalBranchExistsAt` that mirrors the multi-repo lifecycle
  helper but stays scoped to this scenario. State keys for the two
  runs are namespaced (`expired_run_*`, `fresh_run_*`) so they
  cannot collide with the shared `run_id` slot existing helpers
  use.

Test gates:

- `go test -tags scenario -count=1` for the new test on the test
  database: green.
- `go test -race -tags scenario -count=1` for the new test: green.
- `make docker-lint`: 206 issues — same baseline as TASK-011 through
  TASK-015. Zero new findings in `internal/scenariotest/`.
- Mutation verification (drop `AND timeout_at <= $1` from
  `ListTimedOutRuns`): scenario fails on
  `assert-run-status-fresh_run-active` as expected.
- `codex review --uncommitted`: clean — "no discrete correctness
  issues".
- Pre-existing scenariotest suite failures (`TestSchemaMatchesProduction`,
  parallel-suite contention failures) reproduce on `main` without
  this change; not caused here.
