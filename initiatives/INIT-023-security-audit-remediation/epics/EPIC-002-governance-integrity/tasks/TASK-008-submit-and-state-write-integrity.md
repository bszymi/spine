---
id: TASK-008
type: Task
title: "Submit ownership, step-output evidence and compare-and-swap state writes"
status: Pending
work_type: security
created: 2026-10-06
last_updated: 2026-10-06
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
  - type: related_to
    target: /architecture/engine-state-machine.md
  - type: related_to
    target: /architecture/event-schemas.md
  - type: related_to
    target: /architecture/workflow-definition-format.md
---

# TASK-008 — Submit ownership, step-output evidence and compare-and-swap state writes

---

## Purpose

This task makes step results and engine state writes tamper-resistant. It covers three P3 items
from the 2026-10-06 security audit of Spine `61994fe`: **P3-17** (Spine half), **P3-19** and
**P3-24**. All line numbers refer to Spine `61994fe` and SMP `80995559`. TASK-005 and TASK-007
merge before this task is built (see "Dependencies").

1. **P3-17: the engine does not know who submits a result.** Surface: both. Severity P3
   (defence in depth).
   - `SubmitRequest` is `{ExecutionID, OutcomeID, ArtifactsProduced}` (`core/engine/result.go:12-16`).
     `IngestResult` only checks that *some* actor is bound (`:72-76`). `SubmitStepResult` re-reads
     the execution and checks the same thing (`core/engine/step.go:284`, `:295-299`). The owner is
     never compared with the caller.
   - **The callers' checks can be skipped.**
     - The standalone gateway checks ownership only when an actor is in the context *and* a store
       is wired (`service/internal/gateway/handlers_workflow.go:406-407`; design check corrected
       from `:404-405`). The comment at
       `:396-399` says it "fall[s] through to IngestResult rather than failing closed".
     - The adapter drops the identity (`service/cmd/spine/serve_wiring.go:60-65`).
     - SMP's member path skips its check when the context carries no actor or no resolver is wired
       (`internal/customer/spineembedded/proxy.go:1242-1245`).
     - SMP's agent-engine path relies on "IngestResult's 'actor must be non-empty' conflict check"
       only (`internal/engine/adapters.go:235-246`).
   - **Attacker.** Any code path that reaches `IngestResult` with a foreign execution ID while a
     caller-side check is skipped. One example is a future caller of the agent adapter. Another is
     a standalone deployment whose store is not wired.
   - **Impact.**
     - The attacker completes another actor's step with any valid outcome.
     - The attacker can also *fail* it. A submission with missing outputs runs
       `failStepWithClassification` (`result.go:108-117`, `:192-210`). That marks the owner's step
       failed and starts retry or `FailRun`.
   - **Not exploitable today**, because every production caller checks first. The finding is that
     nothing in the engine backs those checks up.

2. **P3-19: `required_outputs` are self-attested labels.** Surface: both.
   - `validateRequiredOutputs` checks only that the submitter *listed* each declared name in
     `artifacts_produced` (`result.go:149-173`).
   - The spec calls these "logical workflow data references" that "the Workflow Engine resolves …
     at runtime" (`architecture/workflow-definition-format.md:347-354`). That runtime binding does
     not exist.
   - **Every shipped value is a label.**
     - Spine `workflows/`:
       - `artifact_content`: `adr-creation.yaml:29`, `artifact-creation.yaml:38`,
         `document-creation.yaml:32`
       - `decision_document`: `adr.yaml:29`
       - `deliverable`: `task-default.yaml:54`
       - `findings`: `task-spike.yaml:26`
       - `summary`: `task-spike.yaml:52`
       - `task_breakdown`: `epic-lifecycle.yaml:29`
       - `workflow_body`: `workflow-lifecycle.yaml:34`, and the `init-repo` seed at
         `service/internal/cli/initrepo.go:456`
     - SMP `workflows/`:
       - `artifact_content`: `adr-creation.yaml:29`, `artifact-creation.yaml:38`,
         `document-creation.yaml:32`
       - `task_breakdown`: `epic-lifecycle.yaml:28`
       - `deliverable`: `task-default.yaml:39`
       - `findings`: `task-spike.yaml:25`
       - `summary`: `task-spike.yaml:50`
       - `workflow_body`: `workflow-lifecycle.yaml:34`
   - **SMP's executors always attest the constant `deliverable`:**
     - runner: `cmd/runner/operational_client.go:139`
     - agent engine: `internal/engine/runner.go:379` and `internal/engine/adapters.go:232`
   - **Impact.**
     - A step that declared outputs can be submitted with nothing pushed.
     - The audit trail then cannot tell it apart from real work: `step_completed` is emitted with a
       **nil payload** (`core/engine/step.go:348-349`; publish step at `core/engine/handlers.go:203-204`).
       That is despite the payload documented in `architecture/event-schemas.md` §4.2.
     - A rework attempt's `step_completed` reuses the first attempt's event ID
       (`evt-<trace>-<step_id>-completed`). The event log's `ON CONFLICT (event_id) DO NOTHING`
       (`adapters/store/postgres_delivery.go:223-229`) therefore drops it. This is code-traced, not
       executed.
   - **This task records evidence; it does not enforce.** Enforcement is descoped (Decision D4,
     Owner question 1).

3. **P3-24: engine state writes are blind read-modify-write.** Surface: standalone. SMP is
   mitigated by per-execution locks and a single replica (`internal/customer/spineembedded/execlock.go:11-37`).
   - **Run status.**
     - `CompleteRun`, `FailRun`, `CancelRun`, `PauseRun` and `ResumeRun` each read the run,
       evaluate the state machine against that snapshot and write with the unconditional
       `UpdateRunStatus` (`core/engine/run.go:398`, `:465`, `:499`, `:533`, `:558`).
       `startRunCommon` (`:99`) and `failRunOnMergeError` (`core/engine/merge.go:457`) do the same.
     - The CAS `TransitionRunStatus` already exists (`core/engine/interfaces.go:26-29`,
       `adapters/store/postgres_runs.go:140-151`). Only `completeAfterMerge`,
       `transitionToPartiallyMerged` and the publish handler use it (`merge.go:350`, `:414`;
       `handlers.go:66`, `:221`).
     - The standalone gateway's no-orchestrator cancel fallback is blind too
       (`service/internal/gateway/handlers_workflow.go:261`).
     - So is the scheduler's run-timeout cancel (`adapters/scheduler/run_timeout.go:61`, after a
       re-read at `:51-59` that leaves a window).
   - **Step executions.**
     - `PostgresStore.UpdateStepExecution` writes whatever snapshot the caller holds
       (`adapters/store/postgres_steps.go:90-107`).
     - Writers in `core/engine`: `result.go:201`, `step.go:78`, `:95`, `:146`, `:342`, `:371`,
       `assign.go:70`, `claim.go:169`, `acknowledge.go:70`, `release.go:76`, `handlers.go:117`,
       `:199` and `merge.go:503`.
     - Writers outside the engine:
       - scheduler: `adapters/scheduler/timeout.go:88` and `recovery.go:198`
       - gateway fallback assigner: `service/internal/gateway/step_assign_fallback.go:93`
       - `core/actor/gateway.go:106`
   - **Impact.**
     - A cancel racing a terminal submit resurrects the cancelled run and merges it.
     - A submit racing the timeout scanner overwrites a failed step and routes its outcome.
     - Two concurrent merge attempts both advance the publish step.

   > **Design-check note (2026-10-06): two P3-24 paths the original design missed.**
   >
   > - **Re-entry on a completed execution. The CAS in §3 does not stop it.**
   >   - `SubmitStepResult` has no terminal guard (`step.go:283-356`). Its only guard, at `:295`,
   >     skips terminal rows.
   >   - The step state machine accepts `step.submit` on `completed` as an idempotent no-op
   >     (`core/workflow/step_machine.go:44-50`).
   >   - So a `SubmitStepResult` that re-reads an execution that is already `completed`
   >     overwrites `OutcomeID` and `CompletedAt` and routes the new outcome a second time.
   >   - §3 takes the CAS expected value *after* that re-read. It is `(completed, actor)`, which
   >     equals the row, so the conditional write applies.
   >   - Paths:
   >     - (a) **Standalone, reachable.** Two submits race on one execution. `IngestResult` reads
   >       `in_progress`, the other submit commits, and `SubmitStepResult` re-reads `completed`.
   >       This includes a submit that races the timeout scanner's `TimeoutOutcome` completion
   >       (`adapters/scheduler/timeout.go:85`).
   >     - (b) **Standalone, reachable.** The scheduler's re-route of a completed step
   >       (`service/cmd/spine/serve_builders.go:300-310`, `serve_resolver.go:169-177`) races a
   >       live replica during a rolling restart.
   >     - (c) **Dormant.** The in-process queue consumer calls `SubmitStepResult` directly
   >       (`core/engine/consumer.go:124`). `NewConsumer` has no production caller.
   >   - **Effect:** a `rejected` review is flipped to `approved` and merged, or the run gets
   >     duplicate next-step executions.
   >   - Both design-check PoCs, `TestTASK008_DC_*` (see "Verified at design time"), fail at
   >     `61994fe` with `outcome rewritten: rejected -> approved` and
   >     `run status=completed, governed merges=1`.
   >   - In SMP, `lockExec` serializes submits at one replica.
   >   - New §3a closes it.
   > - **SMP's `lockRun` has a gap when a run has no live execution.**
   >   - `lockRun` locks only non-terminal executions. It returns a no-op unlock when there are
   >     none (`internal/customer/spineembedded/execlock.go:186-226`).
   >   - Between a submit's terminal write (`step.go:342`) and the creation of the next execution
   >     (`:434`), the live set can be empty, for example at review → publish.
   >   - An SMP cancel then proceeds while the submitting goroutine activates the publish step and
   >     moves the run from `active` to `committing` (`core/engine/handlers.go:66`).
   >   - At HEAD the cancel's blind `UpdateRunStatus` can cancel a `committing` run whose merge
   >     still lands on `main`. This is code-traced, not executed.
   >   - §2's run CAS closes it at the bump: the cancel re-evaluates from `committing` and answers
   >     409. "SMP is mitigated" above is true for step writes, **not** for run status.

**Verified at design time.** The audit shipped no PoC for these three items, so this design wrote
four PoCs. They are `_work/task008/zz_task008_poc_test.go` beside this artifact
(`tmp/init027-orchestration/spine-init023/_work/task008/`). They compile against `61994fe` through
`go test -overlay _work/task008/overlay.json -run TestTASK008_ ./core/engine/`, and **all four fail
there**:

- `…CompleteRunResurrectsCancelledRun`:
  `final status=completed, governed merges=1, status writes=[cancelled committing]`.
- `…SubmitOverwritesTimedOutStep`: the timed-out step is overwritten to `completed`, the `review`
  execution is created, and `step_completed` is emitted.
- `…SubmitHasNoSubmitter`: `SubmitRequest has no ActorID`.
- `…StepCompletedCarriesNoBranchEvidence`: `step_completed has no payload`.

They become the committed regression tests (AC 1, 6, 9, 14). Baseline: `go test ./core/engine/
./adapters/scheduler/ ./service/internal/gateway/ ./core/workflow/ ./adapters/store/` passes at
`61994fe`. SMP `go test ./internal/customer/spineembedded/ ./internal/customer/spinecore/` passes at
`80995559`.

> **Design-check note (2026-10-06): more PoCs and a compatibility prototype.**
>
> - **PoCs.** `_work/task008/designcheck/zz_task008_dc_test.go` reuses the fakes above. Run it with
>   `go test -overlay _work/task008/designcheck/overlay.json -run TestTASK008_DC_ ./core/engine/`.
>   Both tests fail at `61994fe`:
>   - `…DC_SubmitStepResultReroutesCompletedExecution`: a completed `rejected` review, submitted
>     again with `approved`, gives `outcome rewritten: rejected -> approved` and
>     `run status=completed, governed merges=1`.
>   - `…DC_IngestRaceReroutesCompletedExecution`: the same result through `IngestResult` when the
>     winning submit commits between its read and `SubmitStepResult`'s re-read. The response is
>     200 with the loser's outcome.
>
>   They become AC 21.
> - **Compatibility prototype.** `_work/task008/designcheck/overlay-runcas-only.json` contains:
>   - every run mutator in `run.go`/`merge.go` on `TransitionRunStatus`, with a plain conflict on
>     a miss and no retries;
>   - a hard terminal guard in `SubmitStepResult`.
>
>   Results with the prototype:
>   - In Spine, `go test ./core/... ./adapters/scheduler/... ./service/internal/... ./service/cmd/...`
>     and `go run ./cmd/embed-smoketest` pass unchanged. The scenario suite passes, but only 11
>     tests ran: 253 skip without `SPINE_TEST_DATABASE_URL`.
>   - In SMP, `go test -mod=mod ./internal/customer/spineembedded/... ./internal/customer/spinecore/...
>     ./internal/engine/... ./internal/automation/` passes.
>   - The step CAS (§3) and evidence (§4) were not prototyped.
> - **Trap.** SMP has a git-ignored `vendor/` directory that holds a copy of Spine. With it
>   present, `go build`/`go test` default to `-mod=vendor`. They then **silently ignore both
>   `../spine` and any `-overlay`**; this was checked with an overlay that cannot compile, and the
>   build still passed. Every SMP gate in this task runs with `-mod=mod` (AC 19).

**Dependencies.**

- **TASK-005** ("Bind the governed merge to the commit that passed validation and review") rewrites
  the same `IngestResult`/`SubmitStepResult` code. It is also the source of the run-branch head SHA.
  This task needs the following from TASK-005. Check it against TASK-005's artifact when that
  lands, and replace the name if TASK-005 chose another.
  - **(C1)** For every ingested step, TASK-005 resolves the run-branch head SHA server-side and
    persists it on the execution, readable through `ListStepExecutionsByRun`. This document calls
    it `StepExecution.HeadSHA` (`head_sha`).
  - **(C2)** It is set on the in-memory execution before `SubmitStepResult`'s terminal write.
  - If TASK-005 records gate SHAs only (validate, review), it must widen (C1) to every ingested
    step. That is the same code path. This task adds **no** second SHA store and no schema.
  - > **Design-check note (2026-10-06): TASK-005 as designed does not provide (C1).** Checked
    > against `TASK-005-merge-bound-reviewed-sha.md` in this tree.
    >
    > - **What TASK-005 provides.**
    >   - There is no `StepExecution.HeadSHA` and no per-execution column.
    >   - The head is captured server-side by `captureHead` in `SubmitStepResult`, before the
    >     auto-ack and the terminal write. It is returned in memory as `hb.head`, which satisfies
    >     (C2).
    >   - The head is persisted only in reserved `runs.commit_meta` keys, written in
    >     `routeStepOutcome`'s single `SetCommitMeta`:
    >     - `spine.bound_sha` and `spine.bound_by` (`step:<step_id>:<execution_id>`,
    >       `convergence:<id>` or `merge_recovery:<op>`);
    >     - `spine.head.<step_id>`, keyed per **step**, so rework overwrites it.
    > - **When nothing is captured.** No capture happens at all when merge binding is off (no
    >   `EnableMergeBinding`) and the client sends no `expected_head_sha`. That covers SMP until
    >   SMP TASK-009 and `cmd/embed-smoketest`.
    > - **Consequences.** The "walk the run's executions" algorithm in §4 and the run-detail
    >   derivation have no data source. §4 is re-based on TASK-005's real contract (see the §4
    >   note):
    >   - head = `hb.head`;
    >   - previous = `run.CommitMeta["spine.bound_sha"]`, with source `spine.bound_by`, as read
    >     before this submit's write;
    >   - fallback = the baseline;
    >   - binding off → `unknown` / `head_sha_unrecorded`.
    >
    >   Run-detail evidence persists through one new reserved key family,
    >   `spine.evidence.<execution_id>`, in the same `SetCommitMeta` write. That is inside
    >   TASK-005's reserved `spine.` prefix and needs no schema change.
    > - **Coordination.** TASK-005's `headBinding` (or `mergeCommitMeta`) must carry an extra
    >   reserved-key map. Agree this with TASK-005 before either task is built.
- **TASK-007** rewrites `CancelRun` (P3-16: it closes live executions) and the assign/claim entry
  points. This task rebases onto it, and every step write TASK-007 adds goes through this task's
  CAS helper.

## Design

### 0. Invariants

- **No runtime schema change.** Nothing is added under `migrations/` or SMP's `spinemigrations/`.
  The CAS uses the existing `runtime.step_executions.status` and `actor_id` columns and the
  existing `runtime.runs.status`.
- **No interface that SMP implements changes.** `engine.RunStore` (`core/engine/interfaces.go:21-55`)
  and `adapters/store.RunStore` (`adapters/store/interfaces.go:37-69`) keep their method sets.
  SMP passes its `spinecore.RunStore` to `engine.New` as an `engine.RunStore`
  (`internal/customer/spinecore/factory.go:109-117`). `cmd/embed-smoketest/main.go:64-` and a dozen
  test fakes also implement `engine.RunStore`. New behaviour comes from **optional,
  type-asserted** capabilities.
- **Everything is additive for callers.** `SubmitRequest.ActorID` and `StepResult.ActorID` are new
  fields. Empty keeps today's behaviour. SMP's two `SubmitRequest` literals are keyed
  (`proxy.go:1096`, `embedded_ready.go:193`), so they compile unchanged.
- **A lost race never overwrites the winner.** Every engine state write is conditional. On a miss
  the engine re-reads and either re-judges the request against the winning state (run writes) or
  stops without side effects (step writes): no event, no routing, no assignment delivery, no retry
  execution.
- **Evidence never blocks a submission.** Git failures degrade the evidence to `unknown`.

### 1. P3-17: the engine verifies the submitter

`core/engine/result.go`:

```go
type SubmitRequest struct {
	ExecutionID       string
	OutcomeID         string
	ArtifactsProduced []string
	// ActorID is the authenticated submitter. When non-empty, IngestResult
	// refuses (domain.ErrForbidden) a submission for an execution bound to a
	// different actor. Empty = caller-enforced ownership (pre-TASK-008).
	ActorID string
}
```

`core/engine/step.go`: `StepResult` (`:17-20`) gains `ActorID string` with the same meaning.

**Check order in `IngestResult`.** The check runs immediately after the execution is loaded
(`result.go:32`), before the terminal-idempotency branch (`:39-54`). It also runs before every
later check, before TASK-005's head-SHA resolution (so a non-owner costs no git call) and before
`validateRequiredOutputs`.

- `req.ActorID != ""`, `exec.ActorID != ""` and they differ → `domain.NewError(ErrForbidden, "step
  execution %s is not assigned to the submitting actor")`. Nothing is written. This holds on a
  terminal execution too, which matches the gateway rule at `handlers_workflow.go:426-433`.
- `exec.ActorID == ""`: unchanged (the 409 at `:72-76` when live; the idempotent path when
  terminal).
- `req.ActorID == ""`: unchanged.

`IngestResult` then passes `ActorID` into `StepResult`. `SubmitStepResult` repeats the same
comparison after its own re-read (`step.go:284`, next to `:295-299`). That closes the gap between
the two reads. The conditional write in §3 closes the gap between the last read and the write.

**Callers.**

- `service/internal/gateway/server.go:261-265`: `ResultSubmission` gains `ActorID string`.
- `handlers_workflow.go:443-447`: `handleStepSubmit` sets `ActorID: actor.ActorID` whenever
  `actorFromContext` is non-nil, *including when the store is not wired* (`:407`). The gateway's
  own pre-check stays and keeps its earlier 403/409 answers.
- `service/cmd/spine/serve_wiring.go:61-65`: `resultAdapter.IngestResult` forwards `ActorID`.
- `core/engine/consumer.go:117-133`: `executeAndSubmit` passes `ActorID: req.ActorID`, the actor
  the assignment was delivered to. A step rebound while the provider ran is then refused instead
  of completed.
- The crash-recovery callers `service/cmd/spine/serve_builders.go:306` and
  `serve_resolver.go:175` re-route an already-completed execution with an empty `ActorID`. They
  stay unchanged; there is no submitter.
- Dev mode (`actorFromContext == nil`) passes no `ActorID`. Dev mode has no authentication at all.

> **Design-check note (2026-10-06): callers and adjacent sinks.**
>
> - **The engine check is opt-in.** An empty `ActorID` still means "no check". That is the right
>   default at the bump: SMP's two `IngestResult` literals send none until SMP TASK-011, and a
>   fail-closed default would break every SMP submit. The residual is a future caller that
>   forgets the field. §3a's snapshot handoff narrows it: an empty-`ActorID` submit can no longer
>   complete an execution that was rebound between `IngestResult`'s read and the write. Flipping
>   to fail-closed is Owner question 4.
> - **Crash-recovery callers.** `serve_builders.go:306` and `serve_resolver.go:175` stop calling
>   `SubmitStepResult`. They call the new `RecoverCompletedStep` (§3a). `SubmitStepResult` no
>   longer routes a completed execution.
> - **Consumer.** `executeAndSubmit` also maps §3a's "already terminal" conflict to `nil` with an
>   Info log. The memory queue does not redeliver on error (`core/queue/memory.go:217-221`), so
>   this only keeps the log honest.
> - **`FailStep`** (`result.go:177-187`) is a public, actor-less system entry point. SMP calls it
>   from `abandon.go:199`, `automation_recovery.go:569` and `profile_revision.go:240`, behind
>   its own authorization and `lockExec`. The standalone gateway exposes no route to it.
>   - It stays actor-less.
>   - Its contract on a §3 CAS miss: return `nil` and do not retry (`failStepWithClassification`
>     row in §3). That is the same contract as today's "already terminal, nothing to do".
>   - SMP's callers already read the execution and run back after `FailStep` instead of trusting
>     its `nil` (`abandon.go:203-207`, `automation_recovery.go:575-578`), so they stay correct
>     when SMP opts in.
> - **`ReleaseStep` gets the same owner comparison** (cheap guard).
>   - Today it checks only the *assignment row's* actor (`release.go:45-48`), not
>     `exec.ActorID`.
>   - Startup recovery resets `assigned→waiting` without clearing `actor_id` or the assignment
>     row (`adapters/scheduler/recovery.go:186-201`). `AssignStep` then binds B without creating
>     an assignment row.
>   - So A's stale active claim lets A release B's step.
>   - Rule: if `exec.ActorID != ""` and `exec.ActorID != req.ActorID`, return `ErrForbidden`.
>     Nothing is written.
>   - SMP always passes the owner (`execution.go:284` for the member's own step,
>     `automation_recovery.go:393` with `plan.ReleaseActor = f.Owner`, `executor_reconcile.go:457`
>     with `exec.ActorID`), so it is compatible. See AC 23.

### 2. P3-24: run-status writes are compare-and-swap

New helper in `core/engine/state_cas.go`:

```go
var ErrRunStateChanged = errors.New("run changed state concurrently")

const runCASAttempts = 3

// transitionRun reads the run, evaluates req against its CURRENT status and
// writes the result with TransitionRunStatus(from=read status). On a lost
// race it re-reads and re-evaluates, at most runCASAttempts times, so the
// trigger is always judged against the state that won and never written
// over it. Returns the run as read on the winning attempt (Status = from)
// and the transition. A GetRun error is wrapped with %w (the embed
// smoketest relies on errors.Is through CancelRun, cmd/embed-smoketest/main.go:210-216).
func (o *Orchestrator) transitionRun(ctx context.Context, runID string,
	req workflow.TransitionRequest) (*domain.Run, *workflow.TransitionResult, error)
```

When all attempts are lost, it returns `domain.NewErrorWithCause(ErrConflict, …, ErrRunStateChanged)`.
When a re-evaluation fails (for example the run is now `cancelled`), the state machine's own
`ErrConflict` is returned. That is the answer a sequential caller gets today.

**Conversions.** No `o.store.UpdateRunStatus(` call remains in `core/engine`:

| Site | New behaviour on a lost race |
|---|---|
| `CompleteRun` (`run.go:383-449`) | Uses `transitionRun`. No `run_completed` event, no `MergeRunBranch` (`:442`), no cleanup. Returns the conflict to `routeStepOutcome` (`step.go:403`). This is the resurrection fix. |
| `FailRun` (`:452-483`), `CancelRun` (`:486-516`), `PauseRun` (`:519-542`), `ResumeRun` (`:545-567`) | Use `transitionRun`. A cancel that raced a pause re-evaluates from `paused` and still cancels. A cancel that raced a merge start re-evaluates from `committing`, where `run.cancel` is invalid (`core/workflow/run_machine.go:165-200`), and returns 409 (Owner question 2). Events and branch cleanup run only after an applied write. |
| `startRunCommon` (`run.go:92-103`) | `TransitionRunStatus(pending→active)`. On a miss: `ErrConflict`, no `run_started`. |
| `failRunOnMergeError` (`merge.go:443-467`) | `TransitionRunStatus(run.Status→failed)`. A miss logs "run already transitioned" and returns nil, the same as `completeAfterMerge` (`merge.go:354-358`). No `run_failed`. |

**Outside the engine** (all standalone-only; SMP does not import these packages):

- **Gateway cancel fallback** (`handlers_workflow.go:261`): `TransitionRunStatus(run.Status→cancelled)`.
  `!applied` → 409.
- **Scheduler run-timeout** (`run_timeout.go:61`) asserts a local
  `interface{ TransitionRunStatusAt(ctx, runID string, from, to domain.RunStatus, completedAt time.Time) (bool, error) }`.
  `PostgresStore` gains the method (`postgres_runs.go`, next to `UpdateRunStatusAt :127-138`). It is
  the same SQL with `AND status = $4`, so the injected-clock seam documented at
  `adapters/store/interfaces.go:43-52` is kept. On a miss: no `run_timeout` event. A store without
  the method falls back to `UpdateRunStatusAt`.
- **Scheduler startup recovery** (`recovery.go:117`) uses `tx.TransitionRunStatus(pending→active)`.
  It is already on `store.Tx` (`adapters/store/store.go:37`). A miss rolls the transaction back and
  skips the run.

> **Design-check note (2026-10-06).**
>
> - **`failRunOnMergeError`** writes `git_conflict` detail onto the last completed step
>   (`recordGitConflictOnStep`, `merge.go:447-449`) *before* the run write. Move that call after
>   an **applied** run transition, so a lost race leaves no merge-failure detail on a run that
>   another path already finished.
> - **`PauseRun` has no production caller** in either repository (TASK-007 §9). The "cancel that
>   raced a pause" example in this table and in D3 is therefore hypothetical. Re-evaluation is
>   still right, because it is never weaker than the state machine.
> - **Fakes alias run rows.** `mockRunStore.GetRun` returns the stored pointer
>   (`run_test.go:76-83`), so after a CAS write the "as read" run already carries the new
>   status. `transitionRun` must return the `from` status it used separately rather than rely on
>   `run.Status`.
> - **Prototype.** All seven conversions above, done with a plain conflict on a miss, keep every
>   existing Spine unit suite and SMP's hermetic suites green (see "Verified at design time").

### 3. P3-24: step-execution writes are compare-and-swap (optional store capability)

New port in `core/engine/state_cas.go`:

```go
// StepExecutionTransitioner is an OPTIONAL RunStore capability. When the
// store implements it, every engine step write is conditional on the row
// still holding the status and actor the caller read. Stores without it
// (SMP's spinecore.RunStore until SMP TASK-011, test fakes) keep the blind
// UpdateStepExecution. Detected by type assertion only.
type StepExecutionTransitioner interface {
	// TransitionStepExecution writes exec with exactly UpdateStepExecution's
	// column set, only WHERE status = fromStatus AND actor_id IS NOT
	// DISTINCT FROM nullif(fromActorID,''). (true,nil) = applied;
	// (false,nil) = the row exists but moved; domain ErrNotFound = no row.
	TransitionStepExecution(ctx context.Context, exec *domain.StepExecution,
		fromStatus domain.StepExecutionStatus, fromActorID string) (bool, error)
}

var ErrStepStateChanged = errors.New("step execution changed state concurrently")

type stepSnapshot struct{ status domain.StepExecutionStatus; actorID string }
func snapshotStep(e *domain.StepExecution) stepSnapshot

// writeStepExecution persists exec, which the caller loaded as `prior`.
// Capability present → conditional write; a miss returns
// domain.NewErrorWithCause(ErrConflict, …, ErrStepStateChanged) and writes
// nothing. Capability absent → o.store.UpdateStepExecution (unchanged).
func (o *Orchestrator) writeStepExecution(ctx context.Context,
	exec *domain.StepExecution, prior stepSnapshot) error
```

**Store.** In `adapters/store/postgres_steps.go`, the `SET` clause and the argument builder of
`UpdateStepExecution` (`:95-102`) move into a shared constant and function. `TransitionStepExecution`
reuses them and appends `AND status = $10 AND actor_id IS NOT DISTINCT FROM $11`. Any column
TASK-005 adds to the update (for example `head_sha`) therefore flows into the conditional write
automatically. On 0 rows it probes existence (`SELECT EXISTS … WHERE execution_id = $1`) and
returns `ErrNotFound` or `(false, nil)`. That mirrors SMP's `TransitionRunStatus`
(`internal/customer/spinecore/runstore.go:238-251`).

`adapters/store` cannot import `core/engine` (import cycle), so the compile-time assertion
`var _ engine.StepExecutionTransitioner = (*store.PostgresStore)(nil)` lives in
`service/cmd/spine/serve_wiring.go`. Store and engine meet there, and the standalone build breaks if
the store ever stops satisfying the port. The standalone store passed to `engine.New`
(`serve_builders.go:249-252`, `serve_resolver.go:64`) is the `PostgresStore`.

**Engine call sites.** Each captures `prior := snapshotStep(exec)` right after its read and calls
`writeStepExecution`. A miss means:

| Writer | Miss behaviour |
|---|---|
| `SubmitStepResult`: auto-ack (`step.go:371`) and terminal write (`:342`) | Return the conflict before `CompleteAssignment` (`:346`), `step_completed` (`:348`) and `routeStepOutcome` (`:355`). `IngestResult` maps `ErrStepStateChanged` by re-reading. If the execution is now terminal it returns that state with a nil error, exactly as the up-front idempotent path does (`result.go:39-54`). Otherwise it returns the 409. |
| `failStepWithClassification` (`result.go:201`) | Log and return **without** `RetryStep` (`:207`), so no retry execution or `FailRun` for a step someone else resolved. |
| `ActivateStep`: no-actor (`step.go:78`), auto-assign (`:95`), precondition persist (`:146`) | Return the conflict. On the auto-assign path there is no `createAutoAssignmentRecord`, no `DeliverAssignment` (`:99-111`) and no `step_assigned`. |
| `activateInternalStep` (`handlers.go:117`) | Return the conflict. The handler (`:135`) is **not** invoked, so a concurrent activation cannot run the merge handler twice. |
| `advancePublishStepIfAny` (`handlers.go:199`) | Return nil (a sibling attempt won). No `step_completed`, no route-back. This matches the comment at `:173-177`. |
| `AssignStep` (`assign.go:70`), `AcknowledgeStep` (`acknowledge.go:70`), `ReleaseStep` (`release.go:76`) | Return the conflict. |
| `ClaimStep` (`claim.go:169`) | Return the conflict **and** cancel the assignment row it created at `:158-163` (`UpdateAssignmentStatus(cancelled)`). Otherwise the active-assignment unique index blocks every later claim. |
| `recordGitConflictOnStep` (`merge.go:503`) | Log, as today. |
| Any step write TASK-007 adds (for example closing executions in `CancelRun`) | Goes through the helper. A miss means the step already finished, so skip it. |

> **Design-check note (2026-10-06): call-site details.**
>
> - **`SubmitStepResult` re-snapshots after its auto-ack.** The auto-ack moves the row from
>   `assigned` to `in_progress` (`step.go:369-371`). The terminal write must then expect
>   `(in_progress, actor)`, not the `(assigned, actor)` read at `:284`. Otherwise every
>   submission from `assigned` misses once the store has the capability. The §3 fallback suite
>   (AC 11) cannot see this mistake, so AC 22 pins it.
> - **`ReleaseStep` order.** It cancels the assignment row (`release.go:66-70`) *before* the step
>   write (`:76`). Do the conditional step write first and cancel the row only after it applies,
>   so a miss leaves both untouched.
> - **SQL.** Compare `COALESCE(actor_id, '') = $11` with the plain string. Do not use
>   `IS NOT DISTINCT FROM nullif(…)`. Spine always stores NULL for "no actor" (`nilIfEmpty`), but
>   SMP integration fixtures write `actor_id` directly. `COALESCE` treats `''` and NULL alike and
>   avoids a typed-NULL parameter.
> - **`ProcessResult`'s guard.** It refuses *every* terminal status: today only `completed` is
>   idempotent (`core/actor/gateway.go:95-99`), and a `failed` row would be overwritten. A
>   non-empty `exec.ActorID` must also equal `result.ActorID`.
> - **ABA by the same actor is accepted** (D2): release, then reclaim by the same actor. A stale
>   write from that actor's earlier attempt can then match. It cannot cross actors.

**Outside the engine:**

- `adapters/scheduler/timeout.go:88` asserts the capability through a local interface with the
  same method. A miss (a submit won) means: no `step_timeout` event (`:100-111`) and no
  `TimeoutsDetected` increment (`:92`).
- `recovery.go:198` (assigned→waiting on startup) skips on a miss.
- The gateway fallback assigner (`step_assign_fallback.go:93`) returns 409 on a miss.
- `core/actor/gateway.go:80-131` (`ProcessResult`) has no production caller in either repository
  (checked with grep: SMP stubs it as `ErrNotImplemented` at `internal/customer/spinecore/actorassigner.go:100`).
  It still gets the cheap guard: a local-interface conditional write from `in_progress`/`assigned`.
  After this, no blind step writer remains in `core/`.

`PostgresStore.UpdateStepExecution` and `UpdateRunStatus` stay, unchanged, for SMP and for fakes.

### 3a. Terminal re-entry and the `IngestResult` snapshot handoff (design-check addition, 2026-10-06)

The §3 CAS judges a write against the row as `SubmitStepResult` re-read it. That re-read can
already be `completed`, and `step.submit` on `completed` is an accepted no-op transition (see the
Purpose design-check note). Two changes close the gap:

1. **`SubmitStepResult` never routes an execution that is terminal when it reads it.**
   - Right after its re-read (`step.go:284`), it runs these checks in order:
     1. the §1 ownership comparison, when `ActorID` is set. This is 403 even on a terminal row,
        matching the gateway.
     2. the snapshot check in item 2 below.
     3. `exec.Status.IsTerminal()` →
        `domain.NewErrorWithCause(ErrConflict, "step execution … is already <status>", ErrStepStateChanged)`.
        Nothing is written, emitted or routed.
     4. the existing empty-actor check (`:295`).
   - `IngestResult` already maps `ErrStepStateChanged` (§3 table): it re-reads, returns the
     terminal state with a nil error, and otherwise returns 409.
   - The consumer maps the conflict to `nil` (see the §1 note).
2. **`IngestResult` hands its snapshot to `SubmitStepResult`.**
   - `StepResult` gains an **unexported** field, `expect *stepSnapshot`. External keyed literals
     still compile, and only `IngestResult` sets it.
   - When it is set and the re-read row's `(status, actor)` differs from it, the row has moved
     since `IngestResult` validated it. `SubmitStepResult` returns `ErrStepStateChanged` before
     any write. The §3 CAS then uses the re-read value, which equals `expect`.
   - This also closes the empty-`ActorID` window: an execution rebound between the two reads can
     no longer be completed by the earlier submitter.
   - It works with or without the store capability, because it is a read-compare.
3. **New `(*Orchestrator).RecoverCompletedStep(ctx, executionID string) error`.** This is the
   only engine path that routes an already `completed` execution. The two crash-recovery
   callbacks use it. It:
   - loads the execution and refuses unless it is `completed`;
   - loads the run and refuses (`ErrConflict`) unless the run is `active` and
     `run.CurrentStepID == exec.StepID`. That is the scheduler's own precondition
     (`recovery.go:144-180`), re-checked;
   - refuses when any execution of the run was created after this one. This is the successor
     check: a live submit has already routed it, and is still between `CreateStepExecution`
     (`step.go:434`) and `UpdateCurrentStep` (`:439`);
   - performs **no step write** and routes the **stored** `OutcomeID` through `routeStepOutcome`;
   - emits `step_completed` with the same per-execution ID (§4), with
     `branch_change.status = "unknown"` and `reason = "recovered"`. The event log keeps the first
     copy (`ON CONFLICT DO NOTHING`).
   - Two replicas that recover the same execution at once both pass the successor check. Both
     then compute the same deterministic next execution ID (`step.go:425`), so the second
     `CreateStepExecution` fails on the primary key. That is safe.

The prototype of item 1 (a hard terminal guard) leaves every existing Spine unit suite and SMP's
hermetic suites green (see "Verified at design time"). SMP never calls `SubmitStepResult` or the
recovery callbacks, so item 3 is standalone-only.

### 4. P3-19: step-output evidence

New file `core/engine/output_evidence.go`:

```go
type BranchChange struct {
	RepositoryID   string `json:"repository_id"`            // "spine" (primary only, v1)
	Status         string `json:"status"`                   // unchanged|changed|unknown|not_applicable
	HeadSHA        string `json:"head_sha,omitempty"`        // TASK-005's recorded SHA for this execution
	PreviousSHA    string `json:"previous_sha,omitempty"`
	PreviousSource string `json:"previous_source,omitempty"` // "step:<execution_id>" | "baseline"
	ChangedPaths   *int   `json:"changed_paths,omitempty"`   // 0 when unchanged; n when diffed; nil otherwise
	Reason         string `json:"reason,omitempty"`          // head_sha_unrecorded | no_previous_sha |
	                                                          // diff_failed:<git.ErrorKind> | divergence_branch | no_run_branch
}

type StepOutputEvidence struct {
	ExecutionID              string       `json:"execution_id"`
	StepID                   string       `json:"step_id"`
	Attempt                  int          `json:"attempt"`
	ActorID                  string       `json:"actor_id,omitempty"`
	OutcomeID                string       `json:"outcome_id"`
	RequiredOutputs          []string     `json:"required_outputs,omitempty"`
	BranchChange             BranchChange `json:"branch_change"`
	RequiredOutputsUnchanged bool         `json:"required_outputs_unchanged"`
}
```

**Previous recorded SHA.**

- Sort the run's executions by `CreatedAt`, then `ExecutionID` (fakes do not order them).
- Walk the executions created before this one that are `completed`, have `BranchID == ""` and have a
  non-empty `HeadSHA`. The last one gives `PreviousSHA` with source `step:<id>`.
- With none, fall back to `run.RepositoryBaselines["spine"]`, the cut-from tip captured at
  `core/engine/multi_repo_branch.go:76-80` and `:165-172`, with source `baseline`.
- A rework attempt is therefore compared with the step just before it. An `execute` attempt 2
  that pushed nothing since review shows as `unchanged`.

> **Design-check note (2026-10-06): re-based on TASK-005's actual contract.** This replaces the
> "walk the executions" algorithm above, which has no data source (see the Dependencies note).
>
> - **`HeadSHA`** is `hb.head`, the in-memory `headBinding` that TASK-005's `captureHead`
>   returns before the auto-ack. It is empty when `hb == nil`, that is:
>   - binding is off and no `expected_head_sha` was sent;
>   - a divergence-branch step;
>   - an internal step;
>   - a gate rework outcome whose head resolution failed.
>
>   Empty gives `unknown` / `head_sha_unrecorded`.
> - **`PreviousSHA`** is `run.CommitMeta["spine.bound_sha"]` from `SubmitStepResult`'s own
>   `GetRun` (`step.go:301`). That is before `routeStepOutcome` writes this submit's capture.
>   `PreviousSource` is `run.CommitMeta["spine.bound_by"]` verbatim, for example
>   `step:review:run-1-review-1`. The fallback is `run.RepositoryBaselines["spine"]` with source
>   `baseline`; otherwise `no_previous_sha`.
> - **Behaviour is unchanged where it matters.** TASK-005 rewrites `spine.bound_sha` at every
>   capture, so it always holds the latest main-path head before this one. Execute attempt 2,
>   compared with review's capture, is still `unchanged` when nothing was pushed.
> - **Binding off** (SMP until SMP TASK-009, `cmd/embed-smoketest`): every entry is
>   `unknown` / `head_sha_unrecorded`, with no git call and nothing persisted. Evidence follows
>   binding; this task resolves no head of its own.
> - **Persistence for run detail.**
>   - The evidence for this execution is also written as one reserved key,
>     `spine.evidence.<execution_id>`. Its value is the compact JSON of `BranchChange` plus
>     `required_outputs_unchanged`.
>   - It goes into the same `SetCommitMeta` call through TASK-005's `mergeCommitMeta`, which keeps
>     every reserved key of `current`.
>   - `stripReserved` already strips any `spine.`-prefixed key from workflow outcomes, so no
>     workflow can forge one.
>   - The number of keys is bounded by the run's executions (`MaxReworkCycles` × steps).
>   - `merge.go:102` reads only `status`, so the keys never reach the status cascade.

**At submit** (`SubmitStepResult`, after the §3 write is applied, before the event):

- `BranchID != ""` → `not_applicable` / `divergence_branch`. `run.BranchName == ""` →
  `not_applicable` / `no_run_branch`.
- `HeadSHA == ""` → `unknown` / `head_sha_unrecorded`. No previous SHA → `unknown` /
  `no_previous_sha`.
- `HeadSHA == PreviousSHA` → `unchanged`, `ChangedPaths = 0`. **No git call.**
- Otherwise call `o.git.Diff(ctx', PreviousSHA, HeadSHA)` (`core/engine/interfaces.go:167`) under a
  10 s `context.WithTimeout`:
  - success → `changed` with `len(diffs)`;
  - error → `unknown` with `diff_failed:<kind>`. The kind is from `errors.As(*git.GitError)`, else
    `error`. The raw message is logged, never put in the event.
- `RequiredOutputsUnchanged = len(stepDef.RequiredOutputs) > 0 && Status == "unchanged"`. When it is
  true, the engine logs `log.Warn("required outputs declared but run branch unchanged", …)`.

> **Design-check note (2026-10-06): the diff runs on SMP's locked submit path.**
>
> - The diff runs after the step write and before routing, inside SMP's per-execution lock. The
>   request lock budget there is 10 s (`execlock.go:43`). A 10 s diff timeout can therefore turn
>   a concurrent cancel or recovery on the same run into `EXECUTION_BUSY`.
> - It also delays creation of the next step.
> - So use a **3 s** timeout. A timeout costs only the count (`unknown` / `diff_failed:timeout`);
>   the `unchanged` signal needs no git call.
> - SMP's `RealGitOperator.Diff` first force-fetches each argument by *branch name*
>   (`resolveRefForRead`, `gitoperator.go:839-848`). For two object IDs that is two failed
>   fetches before the local fallback.
> - A branch literally named like the SHA would shadow it (P3-7 class). That affects only
>   `changed_paths`, and SMP's ref-qualification task (INIT-027 E4 TASK-005) governs it. Record
>   the dependency; do not work around it here.

**`step_completed` payload.** `step.go:348-349` builds a payload instead of `nil`.

- Event ID: `evt-<trace12>-<execution_id>-completed`. It is now per execution, so rework attempts
  are no longer deduplicated away. A crash-recovery re-route of the same execution still produces
  the same ID, so it stays idempotent.
- Envelope `ActorID`: `exec.ActorID`.
- Fields documented in `architecture/event-schemas.md` §4.2: `step_id`, `step_name`, `outcome`,
  `next_step` (`outcome.NextStep` or `end`), `attempt`, `duration_ms` (when `StartedAt` is set),
  `has_commit` (`len(outcome.Commit) > 0`), `branch_id`.
- New fields:
  - `execution_id`, `actor_id`;
  - `required_outputs` (declared);
  - `artifacts_produced`: the attested list, capped at 32 entries of at most 512 bytes each, plus
    `artifacts_produced_count`, because the list comes from the client;
  - `branch_change`, `required_outputs_unchanged`.
- The engine-owned publish step (`handlers.go:203-204`) emits the documented base fields with the
  same per-execution ID and no `branch_change`.

**Run detail.**

- `(*Orchestrator).StepOutputEvidence(ctx, runID) ([]StepOutputEvidence, error)` reads with
  `GetRun` and `ListStepExecutionsByRun`, and loads the run's pinned workflow once with
  `wfLoader.LoadWorkflow(run.WorkflowPath, run.WorkflowVersion)`. It derives evidence for every
  completed primary-branch execution with **no git call**.
  - `changed` entries carry no count; the count is in the event.
  - The audit-trail copy in `runtime.event_log` is pruned by retention
    (`postgres_delivery.go:295`). The run-detail copy derives from TASK-005's persisted SHAs and is
    not pruned.
- Standalone `GET /runs/{run_id}` (`handleRunStatus`, `handlers_workflow.go:138-216`) resolves the
  reader by type-asserting the workspace `RunCanceller` to a new gateway interface
  `StepOutputEvidenceReader`. That value is the orchestrator in both file and shared mode
  (`serve_wiring.go:443`, `serve_resolver.go:141`), so `ServiceSet` needs no change. When the
  result is non-empty it adds `"step_output_evidence"`. Errors are logged and the key is omitted,
  exactly like the `evidence` block at `:198-214`.

> **Design-check note (2026-10-06).** `StepOutputEvidence` reads the `spine.evidence.<execution_id>`
> keys from `run.CommitMeta`, not per-execution SHAs, which do not exist.
>
> - Each key joins with its execution from `ListStepExecutionsByRun` to add the step, attempt,
>   actor and outcome. `required_outputs` comes from the pinned workflow.
> - An execution without a key is omitted: binding was off, or it ran before this task.
> - There is still no git call. `changed` entries keep the count recorded at submit.
> - Whether run detail is wanted in v1 at all is Owner question 5. The event and the log line
>   carry the finding on their own.

### 5. Documentation

- `KNOWN-LIMITATIONS.md` → "Security Notes" gains **"Step outputs are self-attested labels"**:
  - `required_outputs` are logical labels (`workflow-definition-format.md` §5.3);
  - the runtime binding §5.3 describes is not implemented;
  - the engine only checks that the submitter listed each label (`validateRequiredOutputs`);
  - every shipped value with its `file:line` in both corpora (the list in Purpose 2);
  - SMP's executors attest the constant `deliverable`;
  - what is recorded instead: `branch_change`, `required_outputs_unchanged`, `step_output_evidence`;
  - review remains the control.

  TASK-004 also edits this file (the `safePath` bullet at `:71-74`). The sections differ, so merge
  either way.
- `architecture/event-schemas.md`:
  - §4.2 documents the new `step_completed` fields and the per-execution event ID;
  - the emitter table at `:573` keeps `Orchestrator.SubmitStepResult`.
- `architecture/engine-state-machine.md` gains a short "Concurrent writes" note: run writes are CAS
  with re-evaluation, and step writes are CAS on (status, actor) when the store supports it.

### 6. Data, format and migration

- **Schema:** none.
- **API:** two new optional request fields (`SubmitRequest.ActorID`, `ResultSubmission.ActorID`)
  and one new optional run-detail key.
- **Event format:** `step_completed` changes from a nil payload to an object, and its event ID gains
  the execution ID. No consumer reads either today:
  - SMP has no `step_completed` reader (grep);
  - `internal/customer/runner/event_receiver.go:511` acts only on `step_assigned`, and its audit
    row records `event_type` and `event_id` only (`:505-509`);
  - Spine's internal subscription already lists `step_completed` (`adapters/delivery/bootstrap.go:43-51`),
    so it receives the richer payload with no configuration change.
- **Ownership errors:** a 403 from the engine, where previously the step completed (only for
  callers that pass `ActorID`).

> **Design-check note (2026-10-06): format and behaviour additions.**
>
> - **SMP delivery.** SMP's `smp-internal` subscription *does* include `step_completed`, and an
>   SMP test enqueues one (`internal/customer/workspace/create_initial_actors_test.go:339`).
>   - SMP's receiver therefore now gets one delivery per rework attempt. The delivery queue's
>     `ON CONFLICT (subscription_id, event_id)` (`postgres_delivery.go:16`) used to drop them.
>   - Each delivery carries a payload of at most about 17 KiB. The receiver caps bodies at 1 MiB
>     (`event_receiver.go:322`) and keeps `payload` as `json.RawMessage`, so nothing breaks. It
>     only writes more audit rows.
> - **SSE.** The per-execution ID still embeds the step ID, exactly as today's per-step ID does.
>   SSE `id:` sanitisation is P3-28's owner and is unchanged here.
> - **New engine method.** `RecoverCompletedStep` (§3a). It is standalone-only; SMP does not
>   call it.
> - **Behaviour change.** `SubmitStepResult` on an execution that is terminal when read now
>   returns `ErrConflict` wrapping `ErrStepStateChanged` instead of re-routing it (§3a).
>   `IngestResult` callers see the unchanged idempotent response. No SMP code calls
>   `SubmitStepResult`.

### 7. Backward compatibility

- A store **without** `StepExecutionTransitioner` behaves exactly as at `61994fe`. That covers
  SMP's `spinecore.RunStore` today, `cmd/embed-smoketest` and every existing fake. Existing tests in
  `core/engine` keep passing unchanged. The fakes that implement `TransitionRunStatus`
  (`run_test.go:98-110`, `integration_test.go:54-64`, `orchestrator_test.go:34-36`) already
  implement real CAS or return `(true, nil)`.
- Run CAS uses a method every `engine.RunStore` already implements, so it is active everywhere,
  SMP included. SMP's `TransitionRunStatus` (`runstore.go:221-252`) sets the same lifecycle
  timestamps as its `UpdateRunStatus` (`:197-212`). The projection poller that keys off
  `completed_at` sees no difference.
- Callers that pass no `ActorID` see no change.

### 8. What changes for SMP (SMP TASK-011 adapts; SMP TASK-012 bumps `.spine-revision`)

**At the bump, with no SMP source change**, AC 19 must hold:

- SMP compiles: no interface it implements changed, and its literals are keyed.
- The run-status CAS takes effect through SMP's existing `TransitionRunStatus`. Under `lockRun`
  (`execlock.go:186-226`) it never misses in-process.
  - > **Design-check note (2026-10-06): this is not quite true, and the difference helps SMP.**
    > `lockRun` holds nothing while a run has no live execution (see the Purpose note). An SMP
    > cancel can therefore race the publish handler's `active→committing`, and with this task it
    > now answers 409 instead of cancelling a run that is merging.
    >
    > - The cancel path already maps a state-machine `ErrConflict` to 409 (a sequential cancel of
    >   a `committing` run behaves the same today). No SMP change is needed.
    > - SMP's `TransitionRunStatus` returns `spinecore.ErrRunNotFound`, not `(false, nil)`, for a
    >   missing row (`runstore.go:238-251`). `transitionRun` must propagate a store error and not
    >   retry on it.
- Step writes stay blind until SMP opts in. The per-execution locks keep them safe at one replica.

**SMP TASK-011 must:**

1. **Pass `ActorID` wherever SMP knows the submitter.**
   - Member path: `embeddedRuns.SubmitStep` (`proxy.go:1096-1100`) passes the Spine handle that
     `submitStepActorOwnershipError` (`:1242-`) resolved. That function has to return it.
   - Runner path: `SubmitStepAsRunner` (`embedded_ready.go:193-197`) passes `runnerActorID`, already
     checked at `:169`.
   - Agent engine: the shared `SubmitStep` adapter (`internal/engine/adapters.go:247`) passes the
     actor the agent claimed with. This closes the gap that comment explicitly defers
     (`:235-246`).
     - > **Design-check note (2026-10-06): this is a seam change, not a one-line pass-through.**
       > - The adapter's signature `SubmitStep(ctx, executionID, outcomeID, output)` (`:217`) has
       >   no actor. It dispatches through `embeddedRuns.SubmitStep`, which reads the submitter
       >   only from `auth.ActorIDFrom(ctx)`.
       > - `spineembedded.SubmitStepRequest` has no actor field.
       > - SMP TASK-011 must add the claimed Spine handle to the agent seam and carry it into
       >   `SubmitRequest.ActorID`, through a new request field or a context value. Size SMP
       >   TASK-011 for that.
   - Stuck-step recovery (`internal/automation/stuck_step_recovery.go:102-104`) **stays empty**. It
     is a server-internal system action that submits `failed` on the stuck actor's behalf, and no
     request reaches it. Document that.
2. **Opt into the step CAS (Owner question 3).** Add `func (s *RunStore) TransitionStepExecution(…)`
   delegating to `s.pgs.TransitionStepExecution`, next to `UpdateStepExecution`
   (`runstore.go:366-368`), plus `var _ engine.StepExecutionTransitioner = (*RunStore)(nil)`.
   **Keep** `execlock.go`. The locks serialize in-process. The CAS catches any writer outside them
   and is the protection that survives a second replica.
3. **Optional, default yes.**
   - Surface `StepOutputEvidence` on SMP's RunDetail.
   - Have the step-event receiver write a `workspace_audit_log` row when
     `required_outputs_unchanged` is true, so the admin `/audit` view shows it.
4. **Re-run** AC 19 with the adaptation in place.
5. > **Design-check note (2026-10-06): evidence in SMP depends on SMP TASK-009.** Until SMP
   > TASK-009 enables merge binding, every SMP `step_completed` carries
   > `branch_change.status = "unknown"` (`head_sha_unrecorded`) and run detail has no evidence
   > keys. Item 3 is only useful after SMP TASK-009. Order: SMP TASK-009, then item 3.

## Decisions

- **D1. An optional, type-asserted capability instead of a new method on `engine.RunStore`.**
  - Adding `TransitionStepExecution` to the interface breaks SMP's build at the bump. SMP passes
    `*spinecore.RunStore` to `engine.New` as the `engine.RunStore`
    (`internal/customer/spinecore/factory.go:109-117`). SMP's own structural contract
    (`runstore_test.go:46-62`) would also fall out of step with the engine interface.
  - It also breaks `cmd/embed-smoketest`'s `stubRunStore` and every engine and gateway fake.
  - The optional port keeps all of them compiling. SMP then opts in with three lines in its own
    task, which follows the Spine-first, SMP-second, bump-last order.
- **D2. CAS on (status, actor_id), not on status alone and not on a version column.**
  - A version column needs a migration, which this task excludes.
  - Status alone has an ABA hole: release (`release.go:73-76`, back to `waiting`) followed by
    assign to B returns the row to `assigned`/`in_progress`. A's stale write would then match.
  - Adding the existing `actor_id` column closes that hole with no schema change.
  - > **Design-check note (2026-10-06).** Two cases remain.
    >
    > - **Same-actor ABA is accepted.** Release, then reclaim by the same actor, restores
    >   `(in_progress, A)`. It cannot cross actors.
    > - **`(status, actor)` cannot see a terminal no-op transition.** `completed → completed`
    >   leaves both unchanged. That is why §3a refuses terminal rows before the write instead of
    >   relying on the CAS.
- **D3. Run CAS re-reads and re-evaluates (bounded) instead of failing on the first miss.**
  - Failing at once turns benign interleavings into 409s, for example a cancel that raced a pause.
  - Re-evaluation judges the trigger against the state that won. It never writes over it, so it
    is at least as strict as the state machine and never weaker.
  - Three attempts bound it against a livelock.
- **D4. Evidence, not enforcement, for `required_outputs`.**
  - Treating the values as paths would fail every run in both corpora; they are all labels
    (Purpose 2).
  - Turning them into path globs is a workflow-format change across both repositories plus
    migration of every seeded workflow.
  - Diff-based evidence is honest about what is verifiable today. It adds no false blocks.
- **D5. Event payload plus derived run detail, not a new column.**
  - A column is a schema change.
  - The event log is retention-pruned (`postgres_delivery.go:295`), so the run-detail view derives
    from TASK-005's persisted per-execution SHAs instead.
- **D6. Count changed paths at submit; skip git when nothing changed.**
  - The count is pinned to the two SHAs at submit time. Computing it on read would re-run git for
    every page view.
  - The case that matters (head == previous) needs no git at all.
  - The single diff is time-boxed and best-effort. SMP's `Diff` force-fetches by name and falls
    back to a plain SHA (`gitoperator.go:793-848`), so it may return `unknown` when a SHA is not in
    the persistent clone. That loses the count, not the `unchanged` signal.
- **D7. Check ownership in both `IngestResult` and `SubmitStepResult`.** `SubmitStepResult` re-reads
  the execution (`step.go:284`), and engine-internal callers enter there directly (the consumer at
  `consumer.go:124`). Checking only in `IngestResult` would leave the window between the two
  reads.
- **D8. Per-execution `step_completed` IDs.** The per-step ID silently drops every rework attempt
  (`ON CONFLICT DO NOTHING`), which is exactly the case evidence must show. Crash-recovery
  re-emission keeps the same per-execution ID, so it stays idempotent.
- **D9. Map a lost submit race to the existing terminal response.** That is what a sequential
  duplicate submit gets today (`result.go:39-54`). A new 409 would break SMP's stuck-step recovery,
  which treats 409 as "already resolved" (`stuck_step_recovery.go:109-113`), only by accident of
  timing.
  - > **Design-check note (2026-10-06): the decision stands, but the stuck-step rationale is
    > wrong.** `submitTimeout` treats a 409 as success (`stuck_step_recovery.go:101-116`), and
    > `Recover` logs "stuck step recovered" and posts the notification for both `nil` and 409
    > (`:67-83`). A 409 would therefore not break it. The real reason is parity: a lost race must
    > look exactly like the sequential duplicate that runners and members already get (200 with
    > the persisted state). Clients then never see a timing-dependent status. §3a's
    > "terminal on read" uses the same mapping.
- **D10. Keep the compile-time port assertion in `service/cmd/spine`.** `adapters/store` cannot
  import `core/engine` (the engine imports the store). `make lint-boundaries` (`dev/lint-boundaries.sh`)
  allows only the existing core→adapters crossings, and the port lives in `core/engine`, so no
  new crossing is added.
- **D11 (design check, 2026-10-06). Re-route a completed execution only through an explicit
  recovery entry point.**
  - The alternatives rely on a check that cannot see the problem:
    - a CAS on `(status, actor)` (the terminal transition is a no-op);
    - a version column (schema change).
  - Crash recovery is the only legitimate re-router, so it gets its own method with its own
    preconditions: active run, current step and no successor.
  - `SubmitStepResult` becomes "submit", never "re-route".
  - The snapshot handoff costs one comparison and needs no store capability, so it protects SMP
    before SMP opts into the CAS.
- **D12 (design check, 2026-10-06). Evidence follows TASK-005's capture; there is no second head
  resolver.**
  - Resolving the head again when binding is off would add a git call on SMP's locked submit path
    for a signal SMP gets anyway once SMP TASK-009 enables binding.
  - It would also create a second "head at completion" that could disagree with the bound SHA.

## Owner questions

1. **Should a step that declares `required_outputs` but changed nothing be refused rather than
   flagged?**
   - **Default: flag only** (`required_outputs_unchanged`, a warning log and the run-detail
     evidence), with review as the control.
   - Refusing would block legitimate cases, for example a spike whose `findings` live in the
     submission rather than the branch.
   - It would also need path-typed outputs (D4). If wanted later, it becomes an opt-in
     follow-up.
2. **Is it acceptable that a cancel which loses the race to a merge start now returns 409 instead
   of cancelling a `committing` run?**
   - **Default: yes.** The state machine already forbids `run.cancel` from `committing`
     (`run_machine.go:165-200`).
   - At HEAD the blind write could cancel a run whose merge then landed on `main`. The operator
     retries after the merge finishes or fails.
3. **Should SMP opt into the step-execution CAS in SMP TASK-011?**
   - **Default: yes.** It is a three-line delegate, and the per-execution locks stay.
   - Without it, a second customer-app replica would have no step-level protection.
4. **(Design check, 2026-10-06) Should an empty `SubmitRequest.ActorID` become fail-closed once
   every caller sends one?**
   - **Default: yes, as a follow-up after SMP TASK-011.** It would come in with an Orchestrator
     option such as `RequireSubmitter`, plus an explicit system marker such as
     `engine.WithSystemSubmit(ctx)` for the actor-less system callers. Those are SMP's stuck-step
     recovery and the standalone dev mode. `FailStep` and `RecoverCompletedStep` are separate
     methods and are not affected.
   - Not in this task: SMP sends no `ActorID` until SMP TASK-011, so a fail-closed default would
     break every SMP submit at the bump.
   - This task narrows the residual with §3a's snapshot handoff.
5. **(Design check, 2026-10-06) Is run-detail evidence (`step_output_evidence`) needed in v1?**
   - **Default: yes, through the `spine.evidence.<execution_id>` reserved `commit_meta` keys
     (§4 note).** It survives event-log retention and needs no schema change.
   - Alternative: drop run detail and rely on the `step_completed` payload and the Warn log. That
     is cheaper, needs no coordination with TASK-005's `mergeCommitMeta`, and the evidence is lost
     at event-log retention.
   - Either way, P3-19's recording half is met by the event.

## Acceptance Criteria

Each new test below fails on `61994fe`. The PoC versions were verified failing at design time; the
remaining tests are new and must likewise fail before the fix. Each test names the mutation that
must make it fail again.

1. **P3-17, engine.** `core/engine/submit_ownership_test.go::TestIngestResult_RefusesNonOwnerSubmitter`
   is the committed form of PoC `TestTASK008_PoC_SubmitHasNoSubmitter`.
   - An `in_progress` execution bound to `actor-a`, submitted with `ActorID: "actor-b"`, returns
     `ErrForbidden`.
   - The stored row is unchanged (status, outcome, `completed_at`). No `step_completed` is emitted
     and no next execution exists.
   - The same submission with `ActorID: "actor-a"`, and with `ActorID: ""`, completes the step as
     today.
   - Mutation: deleting the comparison in `IngestResult` and `SubmitStepResult` fails the test.
2. **P3-17, no griefing via validation.** A non-owner submission that *omits* a required output to
   `actor-a`'s step returns `ErrForbidden`. The step is still `in_progress`, no retry execution
   exists, and the run is still `active`. On HEAD the step is failed with `invalid_result`.
3. **P3-17, parity and re-read.**
   - (a) `ActorID` mismatch on a `completed` execution → `ErrForbidden`. A `completed` execution with
     an empty actor plus any `ActorID` → the idempotent response, as today.
   - (b) A store hook rebinds the execution to `actor-b` between `IngestResult`'s read and
     `SubmitStepResult`'s read. The submit by `actor-a` returns `ErrForbidden` and writes nothing.
   - (c) `Consumer.executeAndSubmit` with `req.ActorID = "actor-a"` on an execution rebound to
     `actor-b` is refused.
   - (d) *(Design check, 2026-10-06.)* This is the same rebind as (b), but with `ActorID: ""`,
     which is SMP's shape until SMP TASK-011.
     - It returns `ErrConflict` (409) wrapping `ErrStepStateChanged`. The row still belongs to
       `actor-b` and nothing is emitted or routed.
     - This holds with and without the store capability.
     - Mutation: dropping the `expect` comparison in `SubmitStepResult` fails it.
4. **P3-17, standalone gateway.** `service/internal/gateway`:
   - (a) `handleStepSubmit` forwards the authenticated actor in `ResultSubmission.ActorID`, and
     `resultAdapter` forwards it into `SubmitRequest.ActorID`. Both are asserted through capturing
     fakes.
   - (b) With **no store wired** (the fail-open branch at `handlers_workflow.go:407`) and an
     engine-backed result handler, a non-owner submit returns 403.
5. **Ordering.** A non-owner submission performs no git call: the recording fake `GitOperator` sees
   zero calls. Ownership is checked before TASK-005's head-SHA resolution and before
   `validateRequiredOutputs`.
6. **P3-24, run resurrection.** `core/engine/state_cas_test.go::TestCompleteRun_DoesNotResurrectCancelledRun`
   is the committed form of PoC `TestTASK008_PoC_CompleteRunResurrectsCancelledRun`.
   - A `CancelRun` committed inside `CompleteRun`'s read leaves the final status `cancelled`.
   - There are zero governed merges and no `run_completed`, and `CompleteRun` returns `ErrConflict`.
   - HEAD output: `status=completed, merges=1, writes=[cancelled committing]`.
   - Mutation: restoring `UpdateRunStatus` in `CompleteRun` fails the test.
7. **P3-24, every run mutator.**
   - `grep -n 'store.UpdateRunStatus(' core/engine/*.go` (non-test) prints nothing.
   - Table test `TestRunMutators_LostRaceNeverOverwrites` covers `CompleteRun` (with and without a
     commit), `FailRun`, `CancelRun`, `PauseRun`, `ResumeRun` and `failRunOnMergeError` (through a
     permanent merge failure). A hook moves the run to `cancelled` between read and write. No
     mutator overwrites it, and none emits its event or runs branch cleanup.
   - `TestCancelRun_ReevaluatesAfterLostRace`: a hook moves `active→paused`. `CancelRun` ends
     `cancelled` with exactly one `run_cancelled`.
   - A hook moves `active→committing`. `CancelRun` returns `ErrConflict` and the run stays
     `committing`.
8. **P3-24, store.** In `adapters/store/postgres_steps_cas_integration_test.go`
   (`//go:build integration`, run by `go test -tags integration ./adapters/store/`), `TransitionStepExecution`:
   - applies when (status, actor) match;
   - returns `(false, nil)` on a status mismatch and on an actor mismatch (including NULL vs
     non-empty);
   - returns `ErrNotFound` for a missing row;
   - persists the same columns as `UpdateStepExecution`, with every column read back;
   - lets exactly one of two goroutines racing the same expectation apply.

   `TransitionRunStatusAt` likewise applies, misses and stamps the supplied `completed_at`.
   `serve_wiring.go` carries `var _ engine.StepExecutionTransitioner = (*store.PostgresStore)(nil)`,
   and `go build ./service/...` fails if it is removed from the store.
9. **P3-24, submit against timeout.** `TestSubmitStepResult_DoesNotOverwriteTimedOutStep` is the
   committed form of PoC `TestTASK008_PoC_SubmitOverwritesTimedOutStep`, with a fake that
   implements the capability.
   - The stored execution stays `failed`.
   - No `review` execution exists and no `step_completed` is emitted.
   - `IngestResult` returns status `failed` with a nil error.
   - Mutation: calling `UpdateStepExecution` in place of `writeStepExecution` fails the test.
10. **P3-24, every step writer.**
    - `grep -n 'store.UpdateStepExecution(' core/engine/*.go` (non-test) prints only the line
      inside `writeStepExecution`.
    - Table test `TestStepWriters_LostRaceHasNoSideEffects` covers every row of the §3 table. It
      asserts, among other things:
      - no `DeliverAssignment` on the activate miss;
      - the handler is not invoked on the `activateInternalStep` miss;
      - nil, with no event, on the publish miss;
      - no retry execution on the `failStepWithClassification` miss;
      - the assignment row is cancelled on the `ClaimStep` miss.
11. **Fallback is unchanged.** With a store lacking the capability (`mockRunStore`), the §3 writers
    call `UpdateStepExecution` exactly as before. `TestStepWrites_FallbackWithoutCapability` records
    the calls. The whole existing `go test ./core/engine/` suite passes without editing any existing
    test.
12. **Scheduler and gateway fallbacks.**
    - A submit that completes the step between `ListActiveStepExecutions` and the timeout write
      leaves it `completed`, with no `step_timeout` event and no `TimeoutsDetected` increment.
      HEAD writes `failed`.
    - A run moved `active→committing` between the run-timeout re-read and its write is not
      cancelled.
    - The startup-recovery miss skips the run.
    - The gateway cancel fallback answers 409 on a lost race.
    - The fallback assigner answers 409 on a lost race.
    - `ProcessResult` does not overwrite a terminal execution. *(Design check:)* that includes a
      `failed` one, which HEAD overwrites to `completed`. It also refuses a `result.ActorID` that
      differs from a non-empty `exec.ActorID`.
13. **TASK-007 interplay.** Any step write TASK-007 added to `CancelRun` goes through
    `writeStepExecution` (covered by AC 10's grep). After TASK-007's cancel, a submit racing it
    leaves no `completed` step routed on the cancelled run, with the fake implementing the
    capability.
14. **P3-19, evidence.** `core/engine/output_evidence_test.go::TestStepCompleted_FlagsRequiredOutputsWithoutBranchChange`
    is the committed form of PoC `TestTASK008_PoC_StepCompletedCarriesNoBranchEvidence`, with
    TASK-005's head-SHA source faked. Each case's `step_completed` payload must match exactly:
    - (a) Recorded head == baseline gives
      `branch_change = {repository_id:"spine", status:"unchanged", head_sha, previous_sha, previous_source:"baseline", changed_paths:0}`
      and `required_outputs_unchanged:true`, with zero `Diff` calls.
    - (b) Head ≠ previous, with the fake `Diff` returning 3 entries, gives `status:"changed"`,
      `changed_paths:3` and `required_outputs_unchanged:false`.
    - (c) A `Diff` error gives `status:"unknown"` and `reason:"diff_failed:<kind>"`, with no
      `changed_paths` and no error text. The submit still succeeds.
    - (d) A rework `execute` attempt 2 is compared with the `review` execution's SHA
      (`previous_source:"step:<id>"`).
    - (e) A step without `required_outputs` never sets `required_outputs_unchanged`.
    - (f) `artifacts_produced` is capped at 32 entries and `artifacts_produced_count` is exact.

    Mutations: replacing `len(diffs)` with `0`, or dropping the `RequiredOutputs` condition, fails
    a case. HEAD output: `step_completed has no payload`.

    *Design-check amendments (2026-10-06), re-based on TASK-005's contract (§4 note):*
    - "TASK-005's head-SHA source faked" means the test uses TASK-005's real `captureHead` with
      binding on and a fake `CommitBinder` whose `ResolveBranchHead` returns the scripted head.
      Previous SHAs come from seeded `run.CommitMeta["spine.bound_sha"]` and `["spine.bound_by"]`.
    - In (d), `previous_source` is TASK-005's `bound_by` verbatim, for example
      `step:review:run-1-review-1`, and `previous_sha` is the bound SHA seeded by the review
      capture.
    - (g) **Binding off** (no `EnableMergeBinding`, no `expected_head_sha`): `branch_change` is
      `{status:"unknown", reason:"head_sha_unrecorded"}`. The fake `CommitBinder` and `Diff` see
      zero calls, and no `spine.evidence.*` key is written.
    - (h) A `Diff` that blocks past **3 s** gives `reason:"diff_failed:timeout"` (or the context
      kind), and the submit still returns. The test uses a fake that blocks on `ctx.Done()`.
    - (i) After the submit, `run.CommitMeta["spine.evidence.<execution_id>"]` decodes to the same
      `branch_change` and `required_outputs_unchanged` as the event. A workflow outcome
      `commit: {spine.evidence.x: …}` is stripped (TASK-005's `stripReserved`).
15. **Event IDs.** Two attempts of the same step emit two distinct `step_completed` IDs containing
    their execution IDs. Re-routing a completed execution through the crash-recovery callback emits
    the same ID again. The publish step's event carries the documented base fields.
    *(Design check:)* "the crash-recovery callback" means `RecoverCompletedStep` (§3a). Its event
    carries `branch_change.status:"unknown"` and `reason:"recovered"`. Against a store that keeps
    the first copy (`ON CONFLICT DO NOTHING`), the logged payload is still the original one.
16. **Run detail.**
    - `GET /runs/{run_id}` on a seeded run returns `step_output_evidence`. The entries have
      `branch_change.status` set and `required_outputs_unchanged:true` for the unchanged
      `execute` step.
    - The handler makes no git call beyond the workflow load.
    - With no reader, or a reader error, the key is absent and the response is still 200.
    - *(Design check:)* the evidence comes from the `spine.evidence.<execution_id>` keys. Deleting
      every `runtime.event_log` row for the run first does not change the response. An execution
      without a key is omitted. (If Owner question 5 is answered "drop run detail", this AC is
      deleted with §4's run-detail part.)
17. **Docs.**
    - `KNOWN-LIMITATIONS.md` has the "Step outputs are self-attested labels" note, listing every
      shipped value with `file:line` in both corpora and naming the evidence fields.
    - `architecture/event-schemas.md` §4.2 documents the new fields and the per-execution ID.
    - `architecture/engine-state-machine.md` has the "Concurrent writes" note.
18. **No schema change.** `git diff --stat main -- migrations/` is empty, and no SMP migration is
    needed.
19. **SMP compatibility gate**, with `../spine` checked out at this task's head and **no SMP source
    change**:
    - In SMP, `go build ./...` and `go vet ./internal/customer/...` pass.
    - `go test ./internal/customer/spineembedded/... ./internal/customer/spinecore/...` passes
      hermetically **and** with `TEST_SPINE_DB` set. Prepare that database with
      `go run ./dev/apply-migrations -set spine-runtime <dsn>`.
    - The DB run must include the cancel flows `proxy_cancelrun_integration_test.go` and
      `stale_cancel_integration_test.go`, and the submit/complete flows
      `submit_run_gate_integration_test.go` and `submitstep_embedded_test.go`.
    - SMP TASK-011 re-runs this gate after its adaptation.
    - *(Design check, 2026-10-06.)* Every SMP command above runs with **`-mod=mod`**, or
      `GOFLAGS=-mod=mod`.
      - A local git-ignored `vendor/` holds a stale copy of Spine. Without the flag, Go uses it
        and silently tests the old Spine.
      - Before running the gate, `go list -mod=mod -m -f '{{.Dir}}' github.com/bszymi/spine` must
        print the `../spine` checkout.
      - The DB run reports zero skipped tests in the four named files.
20. **Spine gates.**
    - `go build ./...`, `go vet ./...` and `go test ./...` pass.
    - `go test -race ./core/engine/ ./adapters/scheduler/ ./service/internal/gateway/` passes.
    - `go test -tags integration ./adapters/store/` passes against the test DB.
    - `make test-scenario` passes, including `cancel_from_partially_merged_test.go`,
      `run_lifecycle_edge_cases_test.go`, `publish_step_test.go` and `standard_run_test.go`.
    - `make lint-boundaries` and `golangci-lint run` report no new findings.
    - `go run ./cmd/embed-smoketest` still passes: `CancelRun` still wraps the store error.
    - *(Design check, 2026-10-06.)* `make test-scenario` runs with `SPINE_TEST_DATABASE_URL` set,
      and `go test -tags scenario -v` reports **zero `--- SKIP`** in the named files. Without the
      database, 253 of 264 scenario tests skip and the target still prints `ok`.
21. *(Design check, 2026-10-06.)* **P3-24, terminal re-entry.**
    `core/engine/state_cas_test.go::TestSubmitStepResult_NeverReroutesCompletedExecution` and
    `::TestIngestResult_LostRaceKeepsWinnerOutcome` are the committed forms of PoCs
    `TestTASK008_DC_SubmitStepResultReroutesCompletedExecution` and
    `TestTASK008_DC_IngestRaceReroutesCompletedExecution`.
    - A completed `rejected` review submitted again with `approved` through `SubmitStepResult`
      returns `ErrConflict` wrapping `ErrStepStateChanged`.
      - The stored outcome stays `rejected` and the run stays `active`.
      - There are zero governed merges, no `step_completed` and no new execution.
    - Through `IngestResult`, with the winner committing between the two reads, the response is
      the idempotent one: status `completed`, outcome `rejected`, nil error. Nothing is routed.
    - Both pass **with and without** the store capability.
    - HEAD output: `outcome rewritten: rejected -> approved` and
      `run status=completed, governed merges=1`.
    - Mutation: deleting the terminal check in `SubmitStepResult` fails both.
    - `RecoverCompletedStep`:
      - routes the stored outcome of a completed current step on an active run, with no step
        write;
      - returns `ErrConflict` when the run is not `active`, when `CurrentStepID` differs, or when
        any later execution exists;
      - is the only non-test caller pattern left for re-routing:
        `grep -n 'SubmitStepResult(' service/cmd/spine/*.go` prints nothing.
22. *(Design check.)* **Auto-ack under the capability.** With a fake that implements
    `StepExecutionTransitioner`, a submission on an `assigned` execution completes and routes.
    The terminal write expects `(in_progress, actor)`. Mutation: re-using the pre-ack snapshot
    for the terminal write fails it.
23. *(Design check.)* **`ReleaseStep` owner guard.** An execution rebound to `actor-b` while
    `actor-a`'s claim assignment is still active:
    - `ReleaseStep{ActorID: actor-a, AssignmentID: <a's>}` returns `ErrForbidden`;
    - the execution stays `(assigned, actor-b)` and the assignment row stays `active`.

    With the capability, a lost race leaves both the row and the assignment untouched, because
    the step write now comes first. SMP's three `ReleaseStep` callers pass the owner and are
    unaffected (AC 19).
24. *(Design check.)* **Run CAS against SMP's store semantics.** A fake `TransitionRunStatus` that
    returns `(false, ErrRunNotFound)`, as SMP's does for a missing row, makes `transitionRun`
    return that error wrapped (`errors.Is` holds) after a single attempt.

## Out of scope / follow-ups

- **Enforcing `required_outputs`** (Owner question 1). A follow-up would add path-typed outputs to
  the workflow format, behind an opt-in flag, with migration of both corpora.
- **Residual window: step write and next-step creation are not one transaction.** A cancel that
  commits between a submit's applied step write and `advanceToNextStep`'s `CreateStepExecution`
  (`step.go:434`) leaves a `waiting` execution on a cancelled run. TASK-007's run-state entry
  checks keep it from being assigned or claimed. SMP's `lockRun` closes the window in the embedded
  deployment. Fully transactional routing is a separate engine change.
  - > **Design-check note (2026-10-06): `lockRun` does *not* close this window.** By the time the
    > window opens, the submitted execution is terminal and not in the live set `lockRun` locks
    > (Purpose note). In SMP the run then stays cancelled, because the run CAS makes the routing's
    > `CompleteRun` or the publish handler miss. The stray `waiting` execution is refused by:
    >
    > - TASK-007's run-status guards;
    > - SMP's submit gate 3b (`proxy.go`, `submitRunGate`);
    > - the job-token mint's `RUN_NOT_ACTIVE`.
    >
    > It is contained, but it is not removed until fully transactional routing lands.
- **(Design check, 2026-10-06) `failStepWithClassification`'s `RetryStep` and the scheduler's
  failed-step recovery `RetryStep`** can still race. Both create the next attempt with a
  deterministic ID (`%s-%s-%d`), so the loser's `CreateStepExecution` fails on the primary key.
  No duplicate is possible. Documented, not changed.
- **Code-repository branches** are not counted in `branch_change` (primary repository only, v1).
  Code-repo merges are TASK-005's and P3-37's subject.
- **Planning runs.** The engine's seed commit (`StartPlanningRun`/`StartWorkflowPlanningRun`
  `onBranch`, `run.go:266-283`) counts towards the first step's diff when no earlier SHA is
  recorded. `previous_source:"baseline"` makes that visible. Recording the seed SHA as the first
  evidence point needs TASK-005's per-run store and is a follow-up.
- **Removing** `UpdateStepExecution`/`UpdateRunStatus` from the store interfaces. That would break
  SMP and fakes, so it is a later, coordinated change.
- **Deleting the dead `core/actor.Gateway.ProcessResult` path.** It gets only the cheap guard here.
- **Multi-replica SMP.** Once SMP opts in (SMP TASK-011), the step CAS is the cross-replica
  protection. The per-execution locks stay process-local by design (`execlock.go:24-29`).

## Design check

An adversarial review on 2026-10-06 against Spine `61994fe` and SMP `80995559`, read-only on both
repositories. All PoCs and prototypes are under `_work/task008/designcheck/`. The designer's text
is kept, and every change is a marked "Design-check note (2026-10-06)" or an AC or decision
labelled "design check".

**Bypass found: the fix as designed did not close P3-24 for re-entry on a completed execution.**
- The §3 CAS snapshot is taken after `SubmitStepResult`'s own re-read.
- `completed + step.submit` is an accepted no-op transition.
- So a submit that loses a race, or a recovery re-route racing a live replica, still overwrites
  the outcome and routes it again.
- Two new PoCs fail at HEAD. In one, a rejected review is flipped to approved and merged.
- **Added** §3a: a terminal guard, an `IngestResult` → `SubmitStepResult` snapshot handoff
  (which also closes the empty-`ActorID` rebind window), and an explicit
  `RecoverCompletedStep` for the two crash-recovery callers.
- **Added** D11 and AC 21. A hard-guard prototype keeps every existing Spine unit suite and SMP's
  hermetic suites green.

**Dependency contract was wrong: TASK-005 provides no per-execution head SHA.**
- TASK-005 stores `spine.bound_sha`, `spine.bound_by` and `spine.head.<step_id>` in `commit_meta`.
  The head is captured only when binding is on.
- §4 is **re-based** accordingly:
  - head = `hb.head`;
  - previous = the pre-write `spine.bound_sha` and `spine.bound_by`;
  - binding off → `unknown`;
  - run-detail evidence persisted as reserved `spine.evidence.<execution_id>` keys.
- **Added** D12, AC 14 (g)–(i), the AC 16 note and Owner question 5. Coordinate the extra
  reserved-key map with TASK-005.

**Claims corrected.**
- The gateway fail-open branch is at `handlers_workflow.go:406-407`, not `:404-405`.
- SMP's `lockRun` does **not** cover a run with no live execution. So in SMP:
  - "the run CAS never misses in-process" is wrong;
  - the run CAS actually fixes a cancel-vs-merge-start race at the bump;
  - the Out-of-scope claim that `lockRun` closes the step-write/next-step window is wrong.
- D9's stuck-step rationale is wrong (409 and `nil` are handled alike). Parity is the real
  reason.
- `PauseRun` has no production caller, so D3's example is hypothetical.

**Compatibility hazards added.**
- `SubmitStepResult` must re-snapshot after its auto-ack (AC 22).
- `ReleaseStep` cancels the assignment before the step write; reorder it.
- Use `COALESCE(actor_id,'')` in the conditional SQL.
- SMP's `TransitionRunStatus` returns `ErrRunNotFound` for a missing row (AC 24).
- `recordGitConflictOnStep` must run after an applied run write.
- Fakes alias run rows.
- The evidence diff runs inside SMP's 10 s per-execution lock, so its timeout is 3 s.
- SMP now receives one `step_completed` delivery per rework attempt. That is harmless.
- SMP TASK-011's agent-engine item is a seam change.
- SMP evidence is `unknown` until SMP TASK-009.

**Adjacent cheap guards added.**
- `ReleaseStep` compares `exec.ActorID` (AC 23): a stale active claim can release another
  actor's step after startup recovery.
- `ProcessResult` refuses every terminal status and a foreign actor.
- `FailStep` is documented as an actor-less system path, and its contract on a CAS miss is
  stated.

**ACs made falsifiable.**
- AC 19: SMP gates run with `-mod=mod`. A stale git-ignored `vendor/` otherwise silently tests
  the old Spine; this was verified with an overlay that cannot compile.
- AC 20: the scenario suite needs `SPINE_TEST_DATABASE_URL` and zero skips. Without it, 253 of
  264 scenarios skip and the target still prints `ok`.

**Checked and confirmed.**
- Every `UpdateStepExecution`/`UpdateRunStatus` writer is listed. The only one not listed is the
  scenario harness fixture `service/scenariotest/engine/workflow_steps.go:204`.
- The `required_outputs` `file:line` lists in both corpora are exact. SMP has no other corpus.
- `step_completed` has no strict consumer. SMP's receiver keeps the payload raw, with a 1 MiB
  cap.
- The run CAS and the terminal guard keep `cmd/embed-smoketest` and SMP's `ErrConflict` → 409
  mapping intact.

## Implementation Notes

