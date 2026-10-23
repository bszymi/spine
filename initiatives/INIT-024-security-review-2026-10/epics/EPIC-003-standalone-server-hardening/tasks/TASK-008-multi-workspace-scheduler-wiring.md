---
id: TASK-008
type: Task
title: "Wire the multi-workspace scheduler into shared-mode spine serve"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-003-standalone-server-hardening/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-003-standalone-server-hardening/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-004-standalone-service-hardening/tasks/TASK-012-shared-worktree-safety.md
---

# TASK-008 — Wire the multi-workspace scheduler into shared-mode spine serve

---

## Purpose

This task closes the open half of **P3-27** from the 2026-10-06 audit, re-confirmed PARTIAL by
the 2026-10-22 review: in the `db` and `platform-binding` resolver modes, `spine serve` builds no
scheduler at all. INIT-023 TASK-012 made `MultiScheduler` bounded (retry budget, per-workspace
time budget, concurrency cap) but **did not wire it in** — `KNOWN-LIMITATIONS.md:1382` records
exactly that.

**This is a functional gap more than a security finding** (initiative owner question 5). It is
kept here because it is the only Spine PARTIAL with a code fix, and the gap has a governance edge:
with no scheduler, step and run timeouts never fire, orphaned runs are never recovered, and a
committing run whose governed merge hit a transient failure is never retried, so it stalls until
an operator intervenes. The task is standalone-only.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| P3-27 (open half) | P3 | Standalone, shared modes (`db`, `platform-binding`). Single (`file`) mode already runs a scheduler. SMP runs its own projection/recovery workers and calls no `MultiScheduler`. | A deployment in a shared mode. No attacker is needed: timeouts, orphan recovery and committing-run merge retries simply do not run. |

## Current code (verified at `a560c210`)

- `service/cmd/spine/serve_wiring.go:460-462`: `buildScheduler` builds a single-store
  `scheduler.Scheduler` only. `rt.Scheduler.Start` runs it (`serve_lifecycle.go:36`). In shared
  mode `deps.Store` is the process store and the per-workspace stores live in the pool, so this
  one scheduler does not see them.
- `adapters/scheduler/multi.go:116` `NewMultiScheduler` exists and is bounded
  (`MaxConcurrentWorkspaces` default 4 `:74`; `commitMaxRetries`; `workspaceBudget`). Its doc says
  "Not constructed by `spine serve` today (owner question 3)" (`:28`).
- It needs a `WorkspacePool`, a `WorkspaceResolver` and a per-workspace `ServiceSet`
  (`multi.go:79-115`) with the scheduler callbacks.
- The per-workspace `ServiceSet` already carries those callbacks:
  `CommitRetryFn`, `CommitExhaustedFn`, `StepRecoveryFn`, `RunFailFn`, `RunTerminatedFn`
  (`service/internal/workspace/pool.go:74-82`), set by `workspaceOrchestratorBuilder`
  (`serve_resolver.go:195-207`). So every piece the `MultiScheduler` needs is built per workspace
  already; nothing wires them together.
- `MultiProjectionSync` **is** wired (`serve_wiring.go:391`, started `serve_lifecycle.go:42`),
  which is the exact pattern this task follows for the scheduler.

## Design

1. **Build `MultiScheduler` in shared mode.**
   - In `serve_wiring.go`, where `buildScheduler` is called today, branch on the mode:
     - `deps.WSServicePool != nil` (shared): build a `MultiScheduler` over thin adapters that
       satisfy `scheduler.WorkspacePool` (`ServicePool.Get`/`Release`) and
       `scheduler.WorkspaceResolver` (`Resolver.List`), mapping each `workspace.ServiceSet` to a
       `scheduler.ServiceSet` with the callbacks it already holds.
     - single mode: unchanged (`buildScheduler`).
   - Add it to the runtime struct and start it in `serve_lifecycle.go`, beside
     `MultiProjSync.Start`, under the same context.
2. **Carry the bounds from config.**
   - `MultiSchedulerConfig` takes `CommitMaxRetries` from `defaultCommitMaxRetries`
     (`serve_builders.go:392`, value 3), the orphan threshold from `deps.OrphanThreshold`, and the
     time and concurrency budgets from their defaults. New env overrides:
     `SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES`, `SPINE_SCHEDULER_WORKSPACE_BUDGET`.
3. **Each workspace scan respects the pool.**
   - `MultiScheduler` already calls `Get`/`Release` per workspace and holds a per-workspace soft
     budget (`multi.go:270-300`); the adapter must `Release` in every path so a scan does not pin
     a pool entry. The task's tests assert the refcount returns to zero after a tick.

## SMP impact

- **None.** SMP does not embed `service/cmd/spine` or `adapters/scheduler`'s multi path, and runs
  its own recovery and projection workers. The SMP gate re-runs and is expected inert.

## Decisions

1. **Wire, do not redesign.** `MultiScheduler` is already bounded and tested (INIT-023 TASK-012).
   This task is the composition INIT-023 deferred, not new scheduler logic.
2. **Mirror `MultiProjectionSync`.** Its wiring (build in shared mode, start in the lifecycle) is
   the model, so the two background services are constructed and torn down the same way.
3. **No multi-replica leadership.** Like the projection sync and every INIT-023 lock, the
   scheduler is in-process. Running two replicas double-scans; that is the documented single-leader
   assumption and is out of scope.

## Owner questions

1. **Keep P3-27 in this security initiative?** *Default:* yes, labelled functional (initiative
   owner question 5). *Alternative:* move to a functional backlog initiative and drop it here.
2. **Default concurrency.** *Default:* `MaxConcurrentWorkspaces` 4 (the existing default).
   *Alternative:* scale with the pool size.

## Acceptance Criteria

1. **Shared mode runs the scheduler** (`service/cmd/spine`,
   `TestServe_SharedModeBuildsMultiScheduler`). In a `db`-mode wiring, the runtime holds a started
   `MultiScheduler`; in single mode it holds the single `Scheduler`, as before. At `a560c210` the
   shared-mode wiring holds no scheduler.
2. **Timeouts fire per workspace** (`TestMultiScheduler_TimesOutRunsAcrossWorkspaces`, driving the
   real `MultiScheduler` over a two-workspace fake pool). A run past its timeout in each workspace
   is failed within one tick, and `TerminateLiveSteps` is called (INIT-023 TASK-007 callback).
3. **Committing runs are retried** (`TestMultiScheduler_RetriesCommittingRun`). A committing run
   whose merge fails transiently is retried up to `CommitMaxRetries` and then exhausted via
   `CommitExhaustedFn`.
4. **Pool refcount released** (`TestMultiScheduler_ReleasesPoolRefEachTick`). After a tick the
   pool's refcount for every scanned workspace is zero.
5. **Bounds honoured** (`TestMultiScheduler_RespectsConcurrencyAndBudget`). No more than
   `MaxConcurrentWorkspaces` scans run at once, and a workspace over its soft budget leaves items
   for the next tick (asserted through the existing resume mechanism).
6. **Single mode unchanged** (`TestServe_SingleModeSchedulerUnchanged`). The `file`-mode wiring is
   byte-for-byte the previous `buildScheduler` path.
7. **Existing suites.** `go test ./service/... ./adapters/scheduler/...` pass.
8. **SMP compatibility gate** (initiative §5) passes with no SMP change (inert).
9. **Documentation.** `KNOWN-LIMITATIONS.md:1382-1384` becomes "wired" (the remaining gap is
   multi-replica only); `docs/integration-guide.md` (the new scheduler env vars) and
   `CHANGELOG.md`.

## Compatibility

- Standalone only. Shared modes gain a running scheduler; single mode is unchanged.
- New env vars: `SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES`, `SPINE_SCHEDULER_WORKSPACE_BUDGET`.

## Out of scope

- Multi-replica scheduler leadership (Decision 3).
- Any change to `scheduler.Scheduler` or `MultiScheduler` behaviour — this is wiring only.
- SMP's recovery workers.

## File overlaps

- `service/cmd/spine/serve_wiring.go`: this task changes the scheduler block (`:460`); TASK-006
  changes the git-handler config (`:515`). Different blocks.
- `service/cmd/spine/serve_lifecycle.go` and the scheduler adapters: this task only.

---

## Implementation Notes

Implemented on branch `init/INIT-024-TASK-008-multi-workspace-scheduler-wiring`, cut from
`init/INIT-024-scaffold` `12376b55` (= Spine local `main` `a560c210` + the INIT-024 artifacts; no code
difference). Diff base: `git diff init/INIT-024-scaffold...HEAD`. Implementation commit `59a49ca3`;
this notes commit follows it. Status In Progress; the orchestrator closes it after merge. Not pushed.

### Owner questions (defaults taken, all pending the owner)

- **OQ1** (keep P3-27 here): default — kept, labelled functional (INIT-024 owner question 5).
- **OQ2** (default concurrency): default — `MaxConcurrentWorkspaces` 4 (the existing default), not
  scaled with the pool size; operators override it with `SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES`.
- **OQ3** (new; raised by this task): *run startup crash recovery (`RecoverOnStartup`) per workspace
  in the shared modes?* Default — **no** (out of this task's wiring scope; recorded as a remaining
  gap in `KNOWN-LIMITATIONS.md`, item 9). Alternative: a `MultiScheduler.RecoverOnStartup` over every
  listed workspace before `ListenAndServe` (db mode only; `platform-binding` cannot enumerate), with
  the multi-replica caveat that it resets `assigned` steps a live replica may have just assigned.
- **OQ4** (new): *an invalid `SPINE_SCHEDULER_*` value* — default: refuse to start, in every mode
  (no silent fallback; the direction TASK-007 takes for `SPINE_ENV`). Alternative: Warn and keep the
  default.

### What was built

- **`service/cmd/spine/serve_scheduler.go`** (new; this task only): `multiSchedulerConfigFromEnv`
  (retry budget `defaultCommitMaxRetries` = 3, `deps.OrphanThreshold`, the two new overrides:
  `SPINE_SCHEDULER_MAX_CONCURRENT_WORKSPACES` 1–256, `SPINE_SCHEDULER_WORKSPACE_BUDGET` a positive
  duration; anything else refuses to start); `buildMultiScheduler` (nil without a ServicePool, i.e.
  single mode; an Info line prints the effective bounds); `schedulerWorkspacePool` (adapts
  `workspace.ServicePool`; refuses — and releases under the id it was taken under — a set whose
  `Config.ID` is not the id asked for); `schedulerServiceSet` (maps the pooled set's store, events
  and the five engine callbacks; a nil router stays a nil interface); `schedulerWorkspaceResolver`
  (adapts `workspace.Resolver.List`; only `StatusActive` configs whose id passes
  `workspace.ValidateID`); `waitMultiScheduler` (Stop + bounded Wait).
- **`serve_wiring.go`** (scheduler block only): `serveRuntime.MultiScheduler`; `buildServerConfig`
  calls `buildMultiScheduler` right after the untouched `buildScheduler` block.
- **`serve_lifecycle.go`** (this task only): `runServer` split into `startBackgroundServices` (single
  mode exactly as before: `RecoverOnStartup` then `Scheduler.Start`; plus `MultiScheduler.Start`
  beside `MultiProjSync.Start`), `stopBackgroundServices` and `drainBackgroundServices` (waits for
  the shared-mode scheduler's scans within the existing 10 s shutdown deadline, after
  `srv.Shutdown`, so serveCmd's deferred `workspaces.Close()` does not close the pool under a merge).
- **`adapters/scheduler/multi.go`** (deviation 1): per-workspace claim (one scan per workspace at a
  time, any kind; a skipped workspace logs a Warn); progress-based tick wait (`TickWait`, default
  `WorkspaceBudget + TimeoutInterval` = 60 s: a tick stops waiting only after that long with no slot
  freed and no scan ended; stalled scans keep running, stay claimed, are named in an Error and do
  not count against later ticks' slots); panic recovery in `runWorkspace` (Error with `scan`,
  `workspace_id`, `panic`, `stack`; the pool reference released first); exported one-tick
  `ScanTimeouts` / `ScanOrphans` / `RunCommitRetry` (Start's ticker branches call them), `Wait(ctx)`,
  `Config()`; idempotent `Stop`. `scheduler.Scheduler` is unchanged.
- `serve_resolver.go`: one stale comment ("wiring it is a follow-up") updated.
- Docs: `KNOWN-LIMITATIONS.md` "Shared Working Tree" item 9 (now "wired; a single leader is
  assumed", with the remaining limits), `docs/integration-guide.md` (shared-mode bullet, the two env
  rows, deactivation, sweep timing), `CHANGELOG.md` (Fixed / Changed (standalone) / Changed
  (embedders)).
- `.github/workflows/ci.yml`: a "must pass, never skip" step in the `scenario-git-http` job running
  `TestServe_DBModeMultiSchedulerFiresAcrossWorkspaces` (the only Postgres-backed evidence).

### Per-AC evidence

Red runs used a `git archive` of `init/INIT-024-scaffold` in the scratch dir
(`tmp/init027-orchestration/i24/t008/base`), logs beside it (`red-base-*.log`).

| AC | Test(s) | At the scaffold base | On the branch |
|---|---|---|---|
| 1 | `TestServe_SharedModeBuildsMultiScheduler` (defaults and overrides; the lifecycle starts it — `Wait` returns only once `Start` ran and returned), `TestServe_PlatformBindingModeBuildsMultiScheduler` (from the env via `buildServeWorkspaces`), `TestServe_DBModeMultiSchedulerFiresAcrossWorkspaces` (integration, real db mode) | does not compile (`rt.MultiScheduler`, `schedulerMaxConcurrentEnv` undefined); behavioural probe `TestProbeT008_SharedModeRuntimeHoldsAScheduler` (scratch, reflection) **FAILS**: "shared-mode (platform-binding) runtime holds no scheduler; serveRuntime fields: [Config, Scheduler, ProjSync, MultiProjSync, DeliveryCancel]" | PASS (probe PASS too) |
| 2 | `TestMultiScheduler_TimesOutRunsAcrossWorkspaces` (`service/cmd/spine`: real `ServicePool`, real pooled builder and per-workspace orchestrators, two workspaces; run cancelled and both live executions `skipped/run_terminated` via `TerminateLiveSteps`, refcount 0); integration: step timeout `failed/timeout` + run timeout cancelled + execution terminated in ws-a and ws-b on their own databases | does not compile | PASS |
| 3 | `TestMultiScheduler_RetriesCommittingRun` (two workspaces; exactly 3 scheduler retries after the inline failure, then one `CommitExhaustedFn` with maxRetries 3, run routed off committing, no further retries); integration: each workspace's committing run retried by its own orchestrator and completed | does not compile | PASS |
| 4 | `TestMultiScheduler_ReleasesPoolRefEachTick` (real `ServicePool.RefCount` 0 after each kind of tick, incl. a panicking and a failing-to-build workspace); refcount checks after every tick in the integration test and the AC 2/lifecycle tests | does not compile | PASS |
| 5 | `TestMultiScheduler_RespectsConcurrencyAndBudget` (peak exactly 3 of 9 with cap 3; a 40-run workspace under a 35 ms budget leaves runs for the next tick, which resumes where it stopped); existing `TestMultiScheduler_BoundedConcurrency` / `_BudgetedScanResumesWhereItStopped` still pass | does not compile | PASS |
| 6 | `TestServe_SingleModeSchedulerUnchanged` (file mode: `Scheduler` with budget 3 + handler, no `MultiScheduler`, same timeout/termination behaviour, also with the new env vars set); `buildScheduler` and its call are untouched in the diff | n/a (unchanged path) | PASS |
| 7 | `go test ./...` (40 packages ok) incl. `./service/... ./adapters/scheduler/...` | — | PASS |
| 8 | SMP compatibility gate (below) | — | PASS (inert) |
| 9 | KNOWN-LIMITATIONS item 9, integration guide, CHANGELOG | — | done |

Regression tests from the adversarial review (base-compatible API, so they run unchanged at the
base):

| Test | At the scaffold base | On the branch |
|---|---|---|
| `TestMultiScheduler_HungWorkspaceDoesNotStarveOthers` | **FAIL** (10.04 s): "b-victim's timed-out run is still "active" 10s after a-hung's merge retry hung: one tenant starved the others' timeouts" | PASS (0.18 s); a-hung's retry entered once |
| `TestMultiScheduler_PanickingWorkspaceIsIsolated` | **FAIL**: `panic: isoStore: a scan of this workspace's data panicked` (test binary dies) | PASS |
| `TestMultiScheduler_OneScanPerWorkspace` | **FAIL**: "2 scans of one workspace ran at once, want 1" | PASS |
| `TestMultiScheduler_WorkspacesChangeAtRuntime` | PASS (coverage, not a regression) | PASS |

Branch-only coverage: `TestMultiScheduler_SlowButProgressingTickScansEveryWorkspace` (a tick longer
than TickWait but always progressing scans all 12 workspaces), `_HungWorkspacesCannotUseUpTheSlots`
(as many hung workspaces as slots stall one tick; the next scans the rest), `_StopDrainsWithoutGoroutineLeak`
(Stop idempotent; Wait reports the deadline while a scan runs, then nil; no pool reference held, no
scan after Stop, goroutine count back to baseline), `_TwoReplicasOverOneDatabase` (Decision 3: the
timeout cancel and termination apply once, the merge retry runs once per replica — pinned),
`_ConfigDefaults`, `TestMultiSchedulerConfigFromEnv` (13 rows; an invalid value refuses also in single
mode), `TestSchedulerWorkspacePool_RefusesAnotherWorkspacesServices`,
`TestSchedulerServiceSet_MapsCallbacksAndNilEvents`, `TestSchedulerWorkspaceResolver_ListsOnlyActiveValidIDs`,
`TestServeLifecycle_StartsAndDrainsMultiScheduler` (real tickers fire through the lifecycle; drain).
The timing-based tests passed 10/10 under `-race -count=10`.

### Adversarial self-review

1. *Can one tenant make the shared scheduler spend its budget, or crash it, so other tenants'
   timeouts never fire?* — **Yes at the base, now closed.** Budgets are per workspace (a tenant
   cannot spend another's), but a tick waited for every workspace and all three kinds of scan share
   one loop: a single item that never returns blocked every tenant forever. That item is reachable:
   `WithWorkingTree` bounds a sequence (5 min) only on a home-branch tree, so a code repository's
   merge + `git push` runs on the scheduler's never-cancelled context with no deadline — a remote
   that accepts the connection and never answers holds it. Fixed by the claim + progress-based
   TickWait (`_HungWorkspaceDoesNotStarveOthers`, red at base; `_HungWorkspacesCannotUseUpTheSlots`).
   A panic in one tenant's scan (the engine slices stored values, e.g. `run.TraceID[:12]` in
   `FailRun`) ended the process every tenant shares, on every tick reaching the data: now recovered
   (`_PanickingWorkspaceIsIsolated`, red at base; `_ReleasesPoolRefEachTick`). Residuals documented
   (item 9): a slow-but-progressing scan lengthens its tick by up to one budget + one wait (timeouts
   late, never lost); a stalled scan keeps what it holds; as many stalled tenants as slots delay the
   others by one wait.
2. *Can a deactivated workspace keep being scheduled?* — Through the operator API, no: the next
   tick skips it (integration: a timed-out run seeded after deactivation stays `active`; unit:
   `_WorkspacesChangeAtRuntime`, the pool refuses an inactive workspace before taking a reference).
   Deactivated any other way it is listed/resolved for up to the 60 s resolver cache TTL; a scan
   already running finishes. Both documented.
3. *Does the scheduler act with the right workspace's services and credentials?* — Yes: each scan
   uses the pooled set's own store and the callbacks of that workspace's orchestrator (built over its
   store, git pool and binding-scoped credential resolver); the adapter refuses a set for another id
   (`TestSchedulerWorkspacePool_RefusesAnotherWorkspacesServices`); the integration test checks every
   row moved on its own workspace database only.

### Deviations (with reasons)

1. **`MultiScheduler` behaviour changed** although the artifact scoped the task as "wiring only":
   the adversarial review (step 5 of the brief) found the hang and panic paths above; the closest
   safe alternative was to fix them where they live. Standalone-only: SMP constructs no
   `MultiScheduler` (its package is compiled into SMP only through `core/engine`'s use of
   `CalculateBackoff`); `scheduler.Scheduler` is unchanged.
2. **New `MultiScheduler` API** (`ScanTimeouts`/`ScanOrphans`/`RunCommitRetry`, `Wait`, `Config`,
   `MultiSchedulerConfig.TickWait`): needed for the shutdown drain and for deterministic tests from
   `service/cmd/spine` (the per-tick function is unexported). `TickWait` has no env variable (the
   artifact names only two); its default derives from the budget.
3. **Lifecycle refactor and shutdown drain** (not in the design): split for testability; the drain
   keeps the pool from being closed under an in-flight merge. Single-mode start/stop is unchanged.
4. **Test placement**: AC 2 and AC 4 live in `service/cmd/spine` (real pool, real orchestrator
   callbacks) rather than over a fake pool only; AC 3 and AC 5 in `adapters/scheduler`.
5. **CI step added** (`ci.yml`, shared with TASK-009) so the db-mode evidence never silently skips.
6. Line references in the artifact (`KNOWN-LIMITATIONS.md:1382`, `serve_wiring.go:460`) were correct
   at the base; item 9 now spans more lines.

### Gates

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh` | OK |
| `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; "engine constructed and exercised" |
| `go vet ./...` plain / `-tags integration` / `-tags scenario` | clean ×3 |
| `go test -p 4 ./...` | 40 packages ok, exit 0 |
| `go test -race -p 4` touched packages (`adapters/scheduler`, `service/cmd/spine`), plain and `-tags integration` (`SPINE_TEST_DATABASE_URL` → `s24008_test`) | ok ×4; timing tests 10/10 |
| scenario-git-http job, locally (`s24008_test`, registry `s24008_reg`): `TestGitHTTP_*` 20 pass, `TestBootstrapInternalAdmin_*` 2, `TestWorkflowLifecycle_*` 7, pooled workflow policy 1, TASK-008 store CAS 8 + scenarios 2, status lifecycle 3 + validator 1, governance guard 1 + store 1, **new** `TestServe_DBModeMultiSchedulerFiresAcrossWorkspaces` 1 | all rc 0, 0 fail, 0 skip; no `ws_*` databases left |
| git-floor in `golang:1.26-bookworm` (`--pull=never`, read-only mounts, tmpfs caches): git 2.39.5; ref-grammar filter, `./adapters/git/... ./core/artifact/ ./core/workflow/`, plus `./adapters/scheduler/` | ok |
| golangci-lint (bundled binary) on `./adapters/scheduler/... ./service/cmd/spine/...` vs base | 9 = 9 plain, 9 = 9 integration (the one new gocritic `rangeValCopy` fixed before commit): **0 new**; `--enable-only=gosec` 0 / 0 |
| `gofmt -l` on changed files | clean |

### SMP compatibility gate

Checkout `i23/s24-T008/smp` (SMP `main` `3addd44b`, `../spine` = this worktree, tree identical to
`59a49ca3`), `GOFLAGS=-mod=readonly`: `go build ./...` ok; `go vet ./...` plain, `-tags integration`,
`-tags scenario ./scenariotest/...`, `-tags faultinject` all clean. `go test -race -p 4` on
`cmd/customer-app`, `internal/customer/{githttp,gitread,projectionwriter,spinecore,spineembedded,stepevents,workspace}/...`,
`internal/customer/run/...`, `internal/engine/...`: 11 packages ok, `gitread` FAIL only on the two
by-design contract tests (`TestClient_ErrorContractEqualsCLIClient/{batch,one-shot}/dangling_gitlink`,
`TestClient_SpineReadFileRendersTheGitlinksPatch`) — **proven pre-existing**: the same two fail with
SMP `main` against the scaffold base (= `a560c210` code) through a scratch `-modfile`. `dev/testgate`
Spine-runtime leg (`spinecore`, `spineembedded`, `runner`, `executionreadiness`; platform DB
`s24008_smp_it`, runtime `s24008_spine_rt`): PASS, 2143 passed, 3 allowed skips (darwin
`ru_maxrss`); githttp leg (`-tags integration`): PASS, 588 passed, 1 allowed skip. No `ws_*`
databases or roles left. SMP references neither `MultiScheduler` nor `adapters/scheduler`
directly: **inert**.

### Codex

| Round | Review sub-session | Model | Result |
|---|---|---|---|
| 1 (on `59a49ca3`) | `01a169f8-4b37-7fe3-8ff4-11170919008a` | `gpt-6.1-sol` | `findings: []`, "patch is correct" (confidence 0.85) — clean; nothing to dispose |

(The final round on the notes commit is recorded in the orchestrator report.)

### Not run

- GitHub CI (not pushed; INIT-024 owner question 1).
- The rest of `make test-scenario` (known ~31 pre-existing failures; only the CI job's filters were
  run): `SPINE_TEST_DATABASE_URL=… SPINE_REGISTRY_TEST_DATABASE_URL=… go test -tags scenario ./service/scenariotest/...`.
- A live `spine serve` in db mode with real tickers (covered by the lifecycle unit test and the
  db-mode integration test driving one tick of each loop):
  `WORKSPACE_RESOLVER=db SPINE_REGISTRY_DATABASE_URL=… SPINE_INSECURE_LOCAL=1 ./bin/spine serve`.

### Files shared with other INIT-024 tasks (for integration)

- `service/cmd/spine/serve_wiring.go` (TASK-006): this task adds the `MultiScheduler` field to
  `serveRuntime`, the `buildMultiScheduler` call after the `buildScheduler` block, and the field in
  the returned struct — no other block touched.
- `.github/workflows/ci.yml` (TASK-009 pins images and actions): one new step appended to the
  `scenario-git-http` job after the governance-guard step.
- `KNOWN-LIMITATIONS.md`, `CHANGELOG.md`, `docs/integration-guide.md` (all tasks): own section / item
  9 / two env rows and the shared-mode bullet.
- `serve_lifecycle.go`, `serve_scheduler*.go`, `adapters/scheduler/*`: this task only;
  `serve_resolver.go`: one comment.
