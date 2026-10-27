---
id: TASK-008
type: Task
title: "Wire the multi-workspace scheduler into shared-mode spine serve"
status: Pending
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
