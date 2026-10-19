---
id: TASK-008
type: Task
title: "Submit ownership, step-output evidence and compare-and-swap state writes"
status: In Progress
work_type: security
created: 2026-10-06
last_updated: 2026-10-15
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
| `ClaimStep` (`claim.go:169`) | Return the conflict **and** remove the assignment row it created at `:158-163`. It must not stay active, or the active-assignment unique index blocks every later claim. It must not stay cancelled either, or TASK-007's separation-of-duties ledger and prior-holder re-offer read it as a binding. With the optional `engine.AssignmentRowDeleter` it is deleted; without the port it is cancelled (`UpdateAssignmentStatus(cancelled)`). *Amended in review round 2 (Deviation 18); this cell first said "cancel the assignment row".* |
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
	                                                          // | head_moved_tree_unchanged (on unchanged; Deviation 17)
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
  - success with at least one entry → `changed` with `len(diffs)`;
  - success with no entry → `unchanged`, `ChangedPaths = 0`, `reason: head_moved_tree_unchanged`
    (the head moved but the tree did not: an empty commit, or a change and its revert). It is
    therefore flagged by the rule below. *Amended in review round 2 (Deviation 17); this bullet
    first read "success → `changed` with `len(diffs)`", and review round 1 finding 4 changed the
    code;*
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

Implemented on branch `init/INIT-023-TASK-008-submit-and-state-write-integrity`, from Spine local
`main` `ae513dca` (origin `61994fe` + the INIT-023 scaffold + waves 1–3 + the codex 6.1
catch-up). Diff base: `git diff main...HEAD`. Status stays In Progress; the orchestrator closes it
after merge.

### Orchestrator rulings recorded (owner questions take their defaults, pending the owner)

- Scope: audit P3-17 (Spine half), P3-19 and P3-24, as designed, with every design-check note
  applied. Built on TASK-005's head capture and `commit_meta` contract (b911c04: the captured
  binding is written before the step completes, in one write) and on TASK-007's opt-in
  `StepEntryPolicy`.
- **OQ1** (default, pending owner): `required_outputs` unchanged is **flagged only** —
  `required_outputs_unchanged`, a Warn log and the run-detail evidence. Nothing is refused.
- **OQ2** (default, pending owner): a cancel that loses the race to a merge start returns **409**
  (the state machine's `ErrConflict` from `committing`).
- **OQ3** (default, pending owner): SMP opts into the step-execution compare-and-swap in **SMP
  TASK-011**. Here the port is opt-in: a store without it keeps the blind `UpdateStepExecution`.
- **OQ4** (default, pending owner): failing closed on an empty `SubmitRequest.ActorID` is a
  **follow-up after SMP TASK-011**, not this task (SMP sends no `ActorID` yet).
- **OQ5** (default, pending owner): run-detail evidence through the reserved
  `spine.evidence.<execution_id>` `commit_meta` keys, written in TASK-005's single binding write
  (`headBinding.extra` → `mergeCommitMeta`), never a second whole-map replace. TASK-005's recovery
  per-run lock (`lockRecoveryBinding`) is untouched: it still serialises only the bound recovery
  ledger; the step write does not take it (KNOWN-LIMITATIONS "Engine State Writes …" 5).

### What was built

| Commit | Content |
|---|---|
| `28da58bc` | `adapters/store`: `TransitionStepExecution` (shared `stepExecUpdateSet` / `stepExecUpdateArgs` with `UpdateStepExecution`; `AND status = $10 AND COALESCE(actor_id,'') = $11`; on 0 rows an `EXISTS` probe → `ErrNotFound` or `(false, nil)`) and `TransitionRunStatusAt` (`UpdateRunStatusAt` + `AND status = $4`); integration tests (AC 8). |
| `1df5cb54` | `core/engine/state_cas.go` (`transitionRun` with `runCASAttempts = 3`, `ErrRunStateChanged`, the optional `StepExecutionTransitioner`, `stepState`/`stateOf`, `writeStepExecution`, `ErrStepStateChanged`); every run mutator on `transitionRun` / `TransitionRunStatus`; every engine step writer on `writeStepExecution` with the §3 miss behaviour; `SubmitRequest.ActorID`, `StepResult.ActorID`, the unexported `StepResult.expect` snapshot handoff, `checkSubmitter`, the §3a terminal guard, `lostSubmitRace` (D9 mapping), `RecoverCompletedStep`; `ReleaseStep` owner guard; the consumer's `ActorID` and conflict mapping; tests (`state_cas_test.go`, `submit_ownership_test.go`, fixture `state_cas_fixture_test.go`). Two TASK-005 tests that pinned the old re-route adapted (Deviation 2). |
| `8b8aeeec` | `core/engine/output_evidence.go` (`BranchChange`, `StepOutputEvidence`, constants, `branchChangeAtSubmit`, `requiredOutputsUnchanged`, `evidenceMetaEntry`, the `step_completed` payload and per-execution id, `Orchestrator.StepOutputEvidence`); `headBinding.extra` in `commitmeta.go`; `emitEventAs`; the publish step's base payload; tests (`output_evidence_test.go`). |
| `4ade922d` | Standalone: gateway forwards `ResultSubmission.ActorID` (also with no store wired), cancel fallback and fallback assigner conditional (409), `StepOutputEvidenceReader` + `step_output_evidence` on `GET /runs/{run_id}`; `resultAdapter` forwards `ActorID`; compile-time `var _ engine.StepExecutionTransitioner = (*store.PostgresStore)(nil)` in `serve_wiring.go`; both recovery callbacks on `RecoverCompletedStep`; scheduler timeout / run-timeout / startup recovery conditional; `core/actor` `ProcessResult` guards; tests, including a PostgreSQL scenario test. |
| `bef50715` | Docs: `event-schemas.md` §4.2 and the emitter table, `engine-state-machine.md` "Concurrent writes" + the completed-step recovery row, `workflow-definition-format.md` §5.3 implementation status, `KNOWN-LIMITATIONS.md` (two sections + the TASK-007 bullet), `api/spec.yaml`, `CHANGELOG.md` ("Changed (embedders)"), `docs/integration-guide.md` §10. |
| `be492156` | gocritic findings on the new code (no behaviour change). |
| `5b7ce6d6`, `e190b4af`, `58bcfd17`, `f1614a9c`, `d88a6f88`, `5921c180` | Review round 1 and codex round 8 (see "Review round 1"): evidence judged on the tree and the Warn after the applied write; the same-actor acknowledge answer and the consumer's in-process guard; the gateway and scenario test fixes; docs; the CI step; the conditional branch-cursor move (`engine.BranchCursorTransitioner`, `(*store.PostgresStore).TransitionBranchCursor`). |
| `1ce429b7`, `55d2a366` | Review round 2 and codex round 10 (see "Review round 2"): a lost claim removes its assignment row (`engine.AssignmentRowDeleter`, `(*store.PostgresStore).DeleteAssignment`, asserted in `serve_wiring.go`; `claim_lost_race_test.go`, `TestDeleteAssignment` in CI's TASK-008 step); docs. |
| `c8b56b34`, `7ce1bbee`, `38b15f36`, `bd3b3c50`, `61a2b9e0`, `ccfe4ea4` | Codex rounds 1–6 fixes (see "Codex review"): recovery resumes a successor routing created but did not point the run at; the conditional cursor move (`engine.RunCursorTransitioner`, `(*store.PostgresStore).TransitionCurrentStep`); `ReleaseStep` compensation; the same-actor acknowledgment is not a conflict; a submit retry keeps its evidence comparison point; the consumer finishes a completed step's incomplete routing and treats only resolved executions as done; branch-aware recovery; recovery claims the routing (pinned successor attempt, create first). |

### Per-AC evidence

| AC | Status | Evidence |
|---|---|---|
| 1 | done | `core/engine/submit_ownership_test.go::TestIngestResult_RefusesNonOwnerSubmitter` (committed form of PoC `…SubmitHasNoSubmitter`): `actor-b` on `actor-a`'s `in_progress` execution → `ErrForbidden`, row deep-equal to before, no `UpdateStepExecution`, no `step_completed`, no `review` execution; `ActorID` `actor-a` and `""` complete and route. Mutation M1 (`checkSubmitter` disabled) kills it. |
| 2 | done | `TestIngestResult_NonOwnerCannotFailStep`: a non-owner submit that omits `deliverable` → 403; execution still `in_progress` with no error detail, no `execute-2`, run `active`. |
| 3 | done | `TestSubmitOwnership_TerminalParityAndReread`: (a) foreign duplicate on a completed execution → 403; a completed execution with no actor + any `ActorID` → the idempotent response; (b) a store hook rebinds to `actor-b` between `IngestResult`'s and `SubmitStepResult`'s reads, `actor-a` submits → 403, no write, no event, no routing (with and without the capability); (c) `Consumer.executeAndSubmit` with `req.ActorID = actor-a` on an execution bound to `actor-b` → 403, untouched; a redelivered assignment for a completed step → nil; (d) the same rebind with `ActorID: ""` → `ErrConflict` wrapping `ErrStepStateChanged`, row still `actor-b`, nothing written/emitted/routed, with and without the capability. Mutation M2 (expect comparison dropped) kills (d). |
| 4 | done | (a) `service/internal/gateway/state_cas_test.go::TestStepSubmit_ForwardsAuthenticatedActor` (capturing handler sees `ActorID: alice`), `service/cmd/spine/serve_submit_actor_test.go::TestResultAdapter_PassesActorID` (a foreign `ActorID` through `resultAdapter` → 403 from the real engine); (b) `TestStepSubmit_EngineRefusesForeignSubmitterWithoutStore`: no `Store` in `ServerConfig`, an engine-backed handler (mirrors `resultAdapter`), `alice` submits `bob`'s step → 403. Mutation M13 (gateway does not forward) kills both gateway tests. |
| 5 | done | `TestIngestResult_NonOwnerMakesNoGitCall`: binding on, an `expected_head_sha` and (second request) a missing output; both 403 and the fake binder/`GitOperator` recorded **zero** calls. |
| 6 | done | `core/engine/state_cas_test.go::TestCompleteRun_DoesNotResurrectCancelledRun` (committed form of PoC `…CompleteRunResurrectsCancelledRun`): a `CancelRun` committed inside `CompleteRun`'s read → final `cancelled`, 0 merges, no `run_completed`, `ErrConflict`, 0 blind writes. Mutation M3 (blind `UpdateRunStatus` restored) kills it. |
| 7 | done | `grep -n 'store.UpdateRunStatus(' core/engine/*.go` (non-test) prints nothing. `TestRunMutators_LostRaceNeverOverwrites` (CompleteRun with and without commit, FailRun, CancelRun, PauseRun, ResumeRun, `failRunOnMergeError` through a permanent merge failure): the run stays `cancelled`, no status write, no event, no `DeleteBranch`, no `git_conflict` detail. `TestCancelRun_ReevaluatesAfterLostRace`: raced a pause → `cancelled` with exactly one `run_cancelled`; raced a merge start → `ErrConflict`, stays `committing`. Plus `TestTransitionRun_GivesUpAfterBoundedAttempts` (three attempts, `ErrRunStateChanged`) and `TestStartRunCommon_ActivationIsConditional`. Mutation M17 (blind `failRunOnMergeError`) kills the table. |
| 8 | done | `adapters/store/postgres_steps_cas_integration_test.go` (`-tags integration`, PostgreSQL 18.6): applies on match with every column read back and equal to `UpdateStepExecution`'s write; `(false, nil)` on status, actor, empty-vs-bound and actor-vs-NULL mismatches (row unchanged); `''` and NULL both match `""`; `ErrNotFound` for a missing row; exactly one of two racing goroutines applies (20 rounds, winner's outcome stored); `TestTransitionRunStatusAt` misses, applies and stamps the supplied `completed_at`. The compile-time assertion is in `serve_wiring.go`; an overlay that renames the store method makes `go build ./service/...` fail ("does not implement engine.StepExecutionTransitioner (missing method TransitionStepExecution)"). |
| 9 | done | `TestSubmitStepResult_DoesNotOverwriteTimedOutStep` (committed form of PoC `…SubmitOverwritesTimedOutStep`, fake with the capability): through `SubmitStepResult` → `ErrStepStateChanged`; through `IngestResult` (timeout after read 1 or 2) → `{status: failed}`, nil error; the step stays `failed`, no `review` execution, no `step_completed`. Mutation M4 (blind write) kills it. |
| 10 | done | `grep -n 'store.UpdateStepExecution(' core/engine/*.go` (non-test) prints only the line inside `writeStepExecution` (`state_cas.go:160` at first; `:288` at review round 2). `TestStepWriters_LostRaceHasNoSideEffects` rows: SubmitStepResult auto-ack, failStepWithClassification (no retry execution, run active), ActivateStep no-actor / auto-assign (no `DeliverAssignment`, no `step_assigned`, no assignment row) / precondition persist, activateInternalStep (handler not run, run stays active), advancePublishStepIfAny (nil, no event, no route-back), AssignStep, AcknowledgeStep (no `step_started`), ReleaseStep (assignment row stays active), ClaimStep (its row cancelled; since review round 2 removed, Deviation 18), recordGitConflictOnStep, terminateLiveSteps. The terminal-write row is AC 9. Mutation M10 (lost claim keeps its row) kills the table. *Review round 2:* `claim_lost_race_test.go::TestClaimStep_LostRaceLeavesNoLedgerRow` covers four cases. Under separation of duties the lost claimant is not a run author: it claims the review, while the executing actor is refused. The prior holder's re-offer survives its own lost claim: another actor is refused, and the holder's next claim binds. Without `engine.AssignmentRowDeleter` the row is cancelled, the documented residual. A failing delete falls back to cancelling. Also `adapters/store::TestDeleteAssignment` (PostgreSQL). Mutants r42 to r45 are killed. |
| 11 | done | `TestStepWrites_FallbackWithoutCapability`: a blind store records exactly `[exec in_progress, exec completed, review-1 waiting]` and no conditional call. The pre-existing `core/engine` suite passes; two TASK-005 tests were edited (Deviation 2). |
| 12 | done | `adapters/scheduler/state_cas_test.go`: `TestScanTimeouts_SubmitWinsTheRace` (stays `completed`, no `step_timeout`, `TimeoutsDetected` unchanged; HEAD writes `failed` — M12 kills it), `TestScanTimeouts_ConditionalWriteApplies`, `TestScanRunTimeouts_RunMovedToCommittingIsNotCancelled` (no event, hook not fired), `TestRecoverPendingRuns_MissSkipsTheRun` (no entry step), `TestRecoverAssignedStep_MissSkips`. Gateway: `TestStoreFallbacks_LostRaceAnswers409` (cancel fallback and fallback assigner 409 after one conditional write). `core/actor/gateway_test.go::TestProcessResult_Guards` (failed / skipped not overwritten, waiting not completed, foreign actor 403, a lost race changes nothing). |
| 13 | done | AC 10's grep covers `terminateLiveSteps`; its row in the table (a submit completed the execution inside the cancel → not overwritten). `TestSubmitRacingTerminatingCancel`: a cancel (TerminateStepsOnCancel) lands after the submit's read → the execution stays `skipped`/`run_terminated`, no `review`, no `step_completed`, run `cancelled`. |
| 14 | done | `core/engine/output_evidence_test.go::TestStepCompleted_FlagsRequiredOutputsWithoutBranchChange` (committed form of PoC `…StepCompletedCarriesNoBranchEvidence`), real `captureHead`, binding on, fake `CommitBinder`: (a) head == baseline → exact `{spine, unchanged, head, previous=baseline, source baseline, changed_paths 0}`, flag true, 0 `Diff`; (b) `changed`, 3, flag false, one `Diff(previous, head)` (M7 kills); (c) `unknown`/`diff_failed:transient`, no count, the git message not in the event, submit succeeds; (d) attempt 2 vs the review's binding, `previous_source` `step:review:run-1-review-1`; (e) review without outputs never flagged, no `required_outputs` key (M8 kills); (f) 40 artifacts → 32, count 40, a long entry cut to a UTF-8 prefix ≤ 512 bytes; (g) binding off → `{unknown, head_sha_unrecorded}`, zero git calls, no `spine.evidence.*` written; (h) a `Diff` blocking on `ctx.Done()` → `diff_failed:timeout`, submit returns (default bound asserted 3 s); (i) the stored `spine.evidence.<id>` decodes to the event's values, in the single `commit_meta` write with the binding (M14 kills), and a workflow `commit: {spine.evidence.…}` is stripped. `TestEvidenceKeysRideTheBindingWrite` (`mergeCommitMeta` keeps earlier keys, drops non-reserved extras). *Review round 1:* (j) a moved head whose diff has no entry → `unchanged`, `changed_paths 0`, `reason head_moved_tree_unchanged`, flagged, one Warn; `output_evidence_git_test.go::TestStepOutputEvidence_EmptyCommitIsFlagged` (real `git.CLIClient`: empty commit and change+revert flagged, a real change not); `TestStepCompleted_WarnOnlyAfterTheSubmitApplied`. |
| 15 | done | `TestStepCompleted_EventIDs`: two `execute` attempts → two ids containing their execution ids (M15 kills); `RecoverCompletedStep` re-emits the same id with `unknown`/`recovered`, and a deduplicating log keeps the original `unchanged` payload; the publish step's event carries the base fields and no `branch_change`. `TestStepCompleted_BasePayload` (every base field, `branch_id: null`, envelope actor). |
| 16 | done | Gateway: `TestRunStatus_StepOutputEvidence` (reader with entries → key; reader error, empty, no reader → absent, 200) and `TestRunStatus_StepOutputEvidenceFromOrchestrator` (the orchestrator as `RunCanceller`; an execution without a key omitted; `casNoGit` records no call). Engine: `TestStepOutputEvidence_FromCommitMeta` (no git call; events cleared; no key → nil without loading the workflow). PostgreSQL + git: scenario `TestStepOutputEvidence_RecordedAndReadBack` (execute 1 unchanged/flagged vs baseline, review changed with 1 path, execute 2 unchanged/flagged vs the review's binding; read back after deleting the run's `runtime.event_log` rows). *Review round 1:* the scenario now subscribes `spine serve`'s `DeliverySubscriber`, so the run's three `step_completed` rows are in `runtime.event_log` and carry the same evidence; the delete removes them (asserted) and run detail is deep-equal before and after. The gateway tests assert zero git calls (`casNoGit.requireNoCall`). |
| 17 | done | `KNOWN-LIMITATIONS.md` "Step Outputs Are Self-Attested Labels" (every shipped value with `file:line` in both corpora — re-located: `initrepo.go:518`; SMP's constant `deliverable`; the evidence fields) and "Engine State Writes and Submit Ownership: What INIT-023 TASK-008 Leaves Open"; `event-schemas.md` §4.2 (fields, per-execution id); `engine-state-machine.md` "Concurrent writes". Also `workflow-definition-format.md` §5.3, `api/spec.yaml`, `CHANGELOG.md`, `docs/integration-guide.md` §10. |
| 18 | done | `git diff --stat main -- migrations/` is empty (0 lines); no SMP migration needed. |
| 19 | done | SMP compatibility gate below; re-run in review round 1 with `GOFLAGS=-mod=mod`, the flag the AC names. |
| 20 | done | Gates below. |
| 21 | done | `TestSubmitStepResult_NeverReroutesCompletedExecution` and `TestIngestResult_LostRaceKeepsWinnerOutcome` (committed forms of the design-check PoCs), each with and without the capability: rejected stays rejected, run active, 0 merges, no `step_completed`, no new execution; through `IngestResult` → `completed`/`rejected`, nil. M5 (terminal guard deleted) kills the first; see Deviation 4 for the second. `TestRecoverCompletedStep` (routes the stored outcome with no write to the completed row; refuses a non-active run, another current step, a later execution of the same step, a successor no longer waiting, more than the successor, a non-completed execution — M16 kills). `grep -n 'SubmitStepResult(' service/cmd/spine/*.go` prints nothing. Scenario `TestCompletedStep_IsNotReRouted` (PostgreSQL). Codex-round additions (Deviation 6): `TestRecoverCompletedStep_ResumesStrandedSuccessor`, `…_DoesNotRewindAMovedCursor`, `…_DivergenceBranch`, `…_ConcurrentRecoveriesRouteOnce`, `…_BranchResumesItsOwnClaim`. |
| 22 | done | `TestSubmitStepResult_AutoAckUnderCapability`: conditional writes are exactly `(assigned, actor-a)→in_progress` then `(in_progress, actor-a)→completed`, both applied; the step routes. M6 (pre-ack snapshot reused) kills it. |
| 23 | done | `TestReleaseStep_OwnerGuard` (with and without the capability): `actor-a`'s stale claim on an execution bound to `actor-b` → 403, execution `(assigned, actor-b)`, assignment `active`; the owner still releases (row cancelled). The lost-race half is AC 10's ReleaseStep row. M9 kills it. |
| 24 | done | `TestTransitionRun_StoreErrorIsNotRetried`: a `TransitionRunStatus` returning `(false, errRunNotFound)` → `CancelRun` error `errors.Is` the store's error after exactly 1 attempt. M11 (retry on error) kills it. |

### Deviations and design points resolved against the real code (with reasons)

1. **Ownership is checked before TASK-007's run-terminated answer in `IngestResult`.** TASK-007
   left "this check stays first" for TASK-008's *terminal guard*, which lives in
   `SubmitStepResult` (§3a) and does come after it; §1 places the ownership check "immediately
   after the execution is loaded … before every later check". So a non-owner submitting to a
   run-terminated execution gets 403 instead of TASK-007's 409 — a non-owner learns nothing about
   the execution. Callers that send no `ActorID` (SMP today) see TASK-007's order unchanged.
2. **Two TASK-005 tests were edited (AC 11 says none).** `merge_binding_test.go`
   `TestRecoveryResubmitAndAssertionOrdering/recovery re-submit of a completed gate` and
   `merge_binding_recovery_test.go` `TestBindingSurvivesAFailureAfterCompletion` were written after
   this design and pinned `SubmitStepResult` re-routing a completed execution — exactly what §3a /
   AC 21 remove. They now assert the refusal and use `RecoverCompletedStep`; what they protect
   (no head resolution on recovery, reserved keys unchanged; the binding survives and the moved
   branch is refused at the review) still holds. No other existing test changed.
3. **Evidence is derived before the step write, not "after the §3 write is applied".** Since
   b911c04 a captured binding is written before the terminal write, and the orchestrator ruling
   puts the evidence key into that same write. So `SubmitStepResult` derives the evidence (with its
   one time-boxed diff) right after `captureHead` and before the auto-acknowledge, carries it in
   `hb.extra` through `recordStepCommitMeta` → `mergeCommitMeta`, and emits `step_completed` only
   after the terminal write applied. A submit that then loses its compare-and-swap has written its
   binding and evidence key like TASK-005's binding (KNOWN-LIMITATIONS "Engine State Writes …" 5);
   `StepOutputEvidence` returns only completed executions. *Review round 1:* the derivation is now
   side-effect free — the `required outputs declared but run branch unchanged` Warn moved after
   the applied terminal write, next to `step_completed`, so only the persisted key is derived
   early.
4. **AC 21's mutation for `TestIngestResult_LostRaceKeepsWinnerOutcome`.** Deleting the terminal
   check alone does not fail it: §3a's snapshot handoff (item 2) independently catches the
   `IngestResult` race, because the row moved from `in_progress` to `completed` between the two
   reads. Deleting the terminal check *and* the handoff fails it (checked with an overlay);
   deleting the terminal check fails `TestSubmitStepResult_NeverReroutesCompletedExecution`.
5. **`ReleaseStep` is reordered only with the store capability.** With a conditional store the step
   is written first and the assignment row cancelled after it applied (design check); with a blind
   store (SMP until SMP TASK-011) the original order — row first — is kept, so the call order and
   failure modes an embedder without the capability sees are unchanged (cross-repo rule 2).
6. **`RecoverCompletedStep` grew beyond §3a item 3 through the codex rounds.** The design says
   "refuses when any execution of the run was created after this one" and relies on "both compute
   the same deterministic next execution ID". Against the real code both were wrong:
   - *Round 1:* refusing every later execution stranded a run that crashed between
     `advanceToNextStep`'s `CreateStepExecution` and `UpdateCurrentStep` — nothing else activates
     the successor, and an internal publish step is never claimed, so its merge never ran (before
     this task the re-route created a second attempt and went on). Exactly one later execution that
     is the outcome's next step on the main path, `waiting`, under the deterministic id is now
     resumed (cursor moved, step activated).
   - *Round 2:* that cursor move is conditional (`engine.RunCursorTransitioner`, implemented by
     `(*store.PostgresStore).TransitionCurrentStep`, asserted in `serve_wiring.go`; a store
     without it re-reads first), so a cursor a live submit moved on is never rewound.
   - *Round 5:* a divergence-branch execution is recovered on its branch's cursor (the run is at
     `divergence:<id>`), routing again, or creating the next execution when the branch cursor moved
     but it was never created.
   - *Round 6:* two recoveries that both pass the checks did **not** collide on the primary key:
     the second ran `advanceToNextStep` after the first had created the successor, so
     `nextAttempt` gave it a second attempt. Recovery now claims the routing — the attempt is
     pinned from the listing its checks used, the successor is created first (one recovery wins
     the primary key, the other answers a conflict), and only the creator moves the cursor
     (conditionally) and activates it; the branch path does the same and finishes its own claim
     when it stopped before moving the branch cursor. A live submit racing a recovery still numbers
     its successor from its own later listing (KNOWN-LIMITATIONS "Engine State Writes …" 7).
7. **`ActivateStep`'s precondition persist logs a lost race** and still returns `ErrPrecondition`
   (the §3 table says "return the conflict" for all three `ActivateStep` writes): the persist is
   best-effort detail on a refusal that is the accurate answer; the row is not written either way.
8. **The previous head is the run's *trusted* binding** (`trustedBinding`: well-formed and naming
   this run, DC-9), not `spine.bound_sha` verbatim; an untrusted binding falls back to the
   baseline, as TASK-005's merge and moved-head check do. A baseline that is not a canonical
   object id is no comparison point (`no_previous_sha`), so nothing malformed reaches `git diff`.
9. **`ProcessResult`** refuses anything that is not `assigned`/`in_progress` (except the idempotent
   `completed`), which covers "every terminal status" and also a `waiting`/`blocked` row that it
   used to complete.
10. **`RecoverCompletedStep` calls `CompleteAssignment`** (best effort), for parity with the old
    re-route through `SubmitStepResult`: a crash before it left the assignment row active.
11. **Line numbers re-located:** the `init-repo` seed's `workflow_body` is at
    `service/internal/cli/initrepo.go:518`; the event log's `ON CONFLICT (event_id) DO NOTHING` is
    `adapters/store/postgres_delivery.go:351-357`; `runtime.event_log` has no `run_id` column (the
    scenario deletes by payload).
12. **The design PoC `TestTASK008_PoC_SubmitOverwritesTimedOutStep` still fails on the branch** as
    written: its fake store has no `TransitionStepExecution`, so it exercises the blind fallback,
    which this task leaves unchanged by design (owner question 3). The committed AC 9 test uses a
    store with the capability. The other five PoCs pass on the branch; all six fail on `main`
    (overlay runs, below).
13. **The queue consumer's lost-race handling** (§1 design-check note: "maps §3a's already-terminal
    conflict to nil"): codex rounds 3–5 refined it — only an execution that is terminal on
    re-read counts as resolved (a live moved execution keeps the error); an ownership refusal for
    an execution already resolved by its new owner is resolved too; and for a completed execution
    the consumer calls `RecoverCompletedStep` first, so a redelivery after a routing failure
    finishes the routing. All dormant: the memory queue does not redeliver and `NewConsumer` has no
    production caller.
14. **The snapshot handoff accepts exactly one move** (codex round 3): the step's own actor
    acknowledging it (`assigned → in_progress`, same actor) between `IngestResult`'s read and
    `SubmitStepResult`'s, or between `SubmitStepResult`'s read and its auto-acknowledge (whose
    compare-and-swap then misses), is not a conflict; the submit continues from the acknowledged
    row. §3a item 2 says any difference refuses; refusing this one turned a runner's own
    acknowledge/submit overlap into a 409 (and dropped a consumer result).
15. **A submit retried after its terminal write failed keeps its comparison point** (codex
    round 3): with binding on, b911c04 writes the binding (and the evidence key) before the terminal
    write, so a retry of the same execution found its own capture as the run's binding. The retry
    now reuses the previous head stored in `spine.evidence.<execution_id>`; without one it is
    `unknown`/`no_previous_sha`.
16. **`ReleaseStep` compensates a failed assignment cancel** (codex round 2): with the step
    capability the step is written first (design check), so a failing cancel would leave an unbound
    execution beside an active row that blocks every later claim. The step is put back,
    conditionally, before the error is returned (KNOWN-LIMITATIONS "Engine State Writes …" 8).
17. **A moved head whose diff has no entry is `unchanged`, not `changed`** (review round 1,
    finding 4, `5b7ce6d6`; recorded here in review round 2). §4 said a successful diff gives
    `changed` with `len(diffs)`, and AC 14 lists only cases (a)–(i). Against the real code that
    let an empty commit, or a change and its revert, move the head and be recorded `changed`,
    `changed_paths 0`, unflagged and unwarned: the step escaped the P3-19 flag without changing
    any content. `branchChangeAtSubmit` now judges the status on the tree. A successful diff with
    no entry gives `unchanged`, `changed_paths 0`, `reason: head_moved_tree_unchanged`
    (`engine.EvidenceReasonTreeUnchanged`). That evidence is flagged, persisted flagged in
    `spine.evidence.<execution_id>` and warned after the applied write. The SHAs and the reason
    keep the move visible. A fifth status value (say `changed_tree_unchanged`) would also close
    the gap but is not used: the flag rule
    (`required_outputs_unchanged = declared && status == "unchanged"`) and every consumer of the
    four-value `status` enum would have to learn it, while the content-equality meaning of
    `unchanged` is what the flag promises. Design §4 is amended in place (the bullet says so).
    AC 14 gains case (j): `TestStepCompleted_FlagsRequiredOutputsWithoutBranchChange` (j) with a
    fake `Diff` returning no entry; `output_evidence_git_test.go::TestStepOutputEvidence_EmptyCommitIsFlagged`
    on the real `git.CLIClient` (an empty commit and a change plus its revert are flagged, a real
    change stays `changed` with 1 path); `TestStepCompleted_WarnOnlyAfterTheSubmitApplied`.
    Mutant r30 (the zero-entry branch dropped) fails the first two. The AC 14 wording is left to
    the owner's AC wording pass (Handoffs). Docs: `event-schemas.md` §4.2, `api/spec.yaml`,
    KNOWN-LIMITATIONS "Step Outputs Are Self-Attested Labels" and the CHANGELOG.
18. **A lost claim removes its assignment row instead of cancelling it** (review round 2, codex
    round 10). The §3 table and AC 10 say the `ClaimStep` miss cancels the row it created. Against
    TASK-007's code a cancelled `claim-<execution>-<actor>` row is not inert, because three readers
    take it as a binding:
    - `runAuthor`, the separation-of-duties ledger, counts claim rows in every status. So the lost
      claimant is refused as a reviewer of a run it never wrote (the codex finding).
    - `releasedSince`, the prior-holder re-offer, reads a `claim-<execution>-<holder>` row in any
      status as "held and released". So a prior holder's lost claim on its re-offered rework
      attempt opens that attempt to every other actor. This weakens `ReofferToPriorHolder`.
    - The id is deterministic and `assignment_id` is the primary key, so that actor could never
      claim the execution again.

    The claim never bound its actor, emitted no `step_assigned` and answered an error, so the row
    is now removed. The new optional `engine.AssignmentRowDeleter` is type-asserted on the
    assignment store, so `engine.AssignmentStore` gains no method.
    `(*store.PostgresStore).DeleteAssignment` deletes only while the row names that execution and
    actor, and is asserted in `serve_wiring.go`. No schema change. Nothing grants rights from an
    active assignment row alone: push admission keys on `step_executions.actor_id`, and
    `ReleaseStep`'s owner guard checks the execution. So removing the row erases no right that was
    ever exercised. The row can only be one this call created: an earlier row with the same id
    makes `CreateAssignment` fail before the step write, so a prior binding's row is never touched.
    Without the port, or when the delete fails, the row is cancelled as before, so no claim is
    blocked. The three effects then remain (KNOWN-LIMITATIONS "Engine State Writes …" 11). The
    path is reachable only together with `engine.StepExecutionTransitioner`, so SMP (blind step
    writes) is unaffected. The closest alternatives are not used:
    - A distinct status needs a CHECK change (schema).
    - Writing the step first and the row after, with the capability, would trade the unique-index
      serialisation for a compensating un-write on a failed row insert.

    Tests: `TestClaimStep_LostRaceLeavesNoLedgerRow` (four sub-tests) and `TestDeleteAssignment`
    (in CI's TASK-008 step). The AC 10 table row now asserts the removal. AC 10's wording is left to
    the owner's pass (Handoffs).

### Gates

Environment: the shared PostgreSQL 18.6 container (`localhost:55630`); only `s008_*` databases were
created (`s008_it`, `s008_scen`, `s008_reg`, `s008_scen_main`, `s008_reg_main`, `s008_ci`,
`s008_smp_it`, `s008_spine_runtime_it`), all dropped afterwards. No role was created or dropped.
Containers ran from the present `golang:1.26-bookworm` image (`--pull=never --rm`), none left.

Every gate below was (re-)run on the final code head `ccfe4ea4`; only these notes were committed
after it.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | `lint-boundaries: OK`; OK; "engine constructed and exercised; library surface is sound." (`CancelRun` still wraps the store error) |
| `go vet ./...`, `-tags integration ./...`, `-tags scenario ./...` | clean |
| `go test -p 4 -count=1 ./...` | 40 packages ok, no failure |
| `go test -race -p 4 -count=1 ./core/engine/... ./core/actor/ ./adapters/store/ ./adapters/scheduler/ ./service/internal/gateway/ ./service/cmd/spine/` | all ok, no race |
| `SPINE_TEST_DATABASE_URL=…/s008_it go test -tags integration -race -p 1 -count=1 -v` on the same six packages | 2733 PASS (subtests counted), 0 SKIP; 1 FAIL: `TestRunModeDatabaseDefault` (known pre-existing, identical on `main`) |
| `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...` (`s008_scen`, registry `s008_reg` migrated from `migrations/registry`) vs a `git archive main` export on `s008_scen_main` / `s008_reg_main` | 31 top-level FAIL on both trees, the **same sorted set** (the known pre-existing 30 + `TestWorkspace_ScopedServices`; e.g. the three `run_lifecycle_edge_cases_test.go` tests abort in fixture seeding — `INIT-EC1` is not a valid artifact id — before any engine code runs). Branch 200 PASS (`main` 198 + the two new scenarios), 0 `--- SKIP`. The named files: `cancel_from_partially_merged_test.go`, `publish_step_test.go`, `standard_run_test.go` all PASS; the `run_lifecycle_edge_cases_test.go` three pass on both trees with their fixture ids made numeric (`-overlay` only). Final run on `ccfe4ea4` (the earlier runs on `4ade922d` and `bd3b3c50` matched too). |
| CI `scenario-git-http` job, its four steps scripted verbatim (`bash -eo pipefail`, own `s008_ci`) | `TestGitHTTP_*` 20 PASS (8 `TestGitHTTP_Push*`), `TestBootstrapInternalAdmin_*` 2 PASS, `TestWorkflowLifecycle_*` 4 PASS, `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` PASS, 0 SKIP |
| CI `git-floor` steps in `golang:1.26-bookworm` (git 2.39.5, read-only mounts, `GOPROXY=off`), plus `./core/engine/ ./adapters/scheduler/ ./adapters/store/ ./core/actor/` | all ok |
| `golangci-lint run --new-from-rev main` (`tmp/init027-orchestration/bin/golangci-lint`) on the touched packages: plain, `--build-tags=integration`, `--build-tags=scenario ./service/scenariotest/...` | 0 issues each (5 gocritic findings on the first pass fixed in `be492156`) |
| `--enable-only=gosec --build-tags=integration,scenario` on the touched packages | 0 issues |
| `gofmt -l` on every changed `.go` file | empty (three touched files — `core/engine/release.go`, `adapters/scheduler/recovery.go`, `core/actor/gateway.go` — had pre-existing import-order drift that `gofmt -w` fixed) |
| `git diff --stat main -- migrations/` | empty |
| Mutation pass (`go test -overlay`, worktree untouched): M1–M17 on the original code, m18–m29 on the codex fixes (plus the AC 21 pair, Deviation 4) | every mutant killed (per AC above, per round below) |
| Design PoCs (`_work/task008/overlay.json` + `designcheck/`, re-targeted at this worktree and at the `main` export) | all six fail on `main` with the design-time messages; on the branch five pass, and `…SubmitOverwritesTimedOutStep` still fails by design (Deviation 12) |

### Codex review

`codex review --base main`, plain — no `-c model=` or any override. Every round's review
sub-session (`session_meta.source` = `{"subagent":"review"}`, under `~/.codex/sessions/2026/10/14/`)
records `"model":"gpt-6.1-sol"`.

| Round | Tree | Review session | Findings and disposition |
|---|---|---|---|
| 1 | `be492156` | `01a13bed-083d-7b53-aae9-65194430ab3f` | P1 (rated P2 here: dormant until a crash in a two-write window) — recovery refused every later execution, so a crash between creating the successor and moving the cursor stranded the run, and an internal publish successor never merged. **Fixed** `c8b56b34` (Deviation 6) with `TestRecoverCompletedStep_ResumesStrandedSuccessor`. |
| 2 | `c8b56b34` | `01a13bf2-9945-7281-b9f0-ad29637c6165` | P2 — the resume's unconditional cursor write could rewind a cursor a live submit moved on: **fixed** `7ce1bbee` (new optional `engine.RunCursorTransitioner`, `(*store.PostgresStore).TransitionCurrentStep`, asserted in `serve_wiring.go`; `TestRecoverCompletedStep_DoesNotRewindAMovedCursor`, `TestTransitionCurrentStep`). P2 — with the step capability a failed assignment cancel left an unbound execution beside an active row: **fixed** `7ce1bbee` (conditional compensation; `TestReleaseStep_FailedCancelLeavesConsistentState`). |
| 3 | `7ce1bbee` | `01a13bfb-6369-7cb3-b26d-83876c7b9c1e` | P2 — a same-actor acknowledgment racing the submit made it lose its compare-and-swap; the consumer dropped the result and `IngestResult` answered 409: **fixed** `38b15f36` (`acknowledgedOrSame`; the consumer treats only a terminal re-read as resolved; `TestSubmit_ConcurrentAcknowledgeIsNotAConflict`). P2 — a submit retried after its terminal write failed compared with its own binding (false `unchanged`): **fixed** `38b15f36` (the retry reuses the comparison point stored in `spine.evidence.<id>`; `TestStepCompleted_RetryKeepsTheComparisonPoint`). |
| 4 | `38b15f36` | `01a13c02-ca48-7152-8ceb-63c75ad9b154` | P2 (dormant: the memory queue does not redeliver, `NewConsumer` has no production caller) — a redelivered assignment for a completed step whose routing failed was acknowledged without routing: **fixed** `bd3b3c50` (the consumer calls `RecoverCompletedStep`; `TestConsumer_RedeliveryFinishesIncompleteRouting`). |
| 5 | `bd3b3c50` | `01a13c07-f1c9-7b22-a45b-8d6c152860c8` | P2 (dormant) — a completed divergence-branch execution could not be recovered (run cursor `divergence:<id>`): **fixed** `61a2b9e0` (branch-aware recovery; `TestRecoverCompletedStep_DivergenceBranch`). P2 (dormant) — a stale delivery whose execution was rebound and then resolved stayed a retryable 403: **fixed** `61a2b9e0` (`TestConsumer_StaleDeliveryAfterRebind`). |
| 6 | `61a2b9e0` | `01a13c11-7233-7d30-a9e5-a398d71eba92` | P1 (rated P2 here: two concurrent recoveries of one execution) — the second recovery created a second successor attempt and could rewind the cursor: **fixed** `ccfe4ea4` (recovery claims the routing; `TestRecoverCompletedStep_ConcurrentRecoveriesRouteOnce` main path and branch, `…_BranchResumesItsOwnClaim`). |
| 7 | `ccfe4ea4` | `01a13c17-0e5a-7692-b19f-0e4586c4cb74` | "No actionable regressions were identified in the changes against the supplied merge base." |
| 8 | `f1614a9c` (+ the uncommitted CI step) | `01a13c66-6591-71b1-8381-2c6d08e8b106` | P2 (dormant: only the queue consumer reaches branch recovery) — `recoverBranchStep` wrote its branch snapshot back with a plain `UpdateBranch`, so of two recoveries finishing one stranded claim the late one could reopen a branch the successor had completed: **fixed** `5921c180` (Review round 1, codex). |
| 9 | `5921c180` | `01a13c7a-c11a-7393-b222-10be14b404aa` | "No actionable regressions were identified." |
| 10–12 | `7c54dc82` → `55d2a366` | see "Review round 2" | P2 (a lost claim's cancelled row read as a binding): **fixed** `1ce429b7`; rounds 11 and 12 clean. |

No finding was rejected. Each fix landed with a test that fails when the fix is reverted (overlay
mutations m18–m29; rounds 8–9: r39–r41 below). Rounds 4 and 5 were in the queue consumer, which is dormant (no redelivering
queue, no production caller); they were fixed anyway because the fixes are local.

### SMP compatibility gate (AC 19)

SMP checkout `i23/s-T008/smp`, detached at SMP `main` `1ae9b0e3` (`.spine-revision` `d23b897`),
**no SMP source change** (`git status` clean after every step), no `vendor/`, `GOFLAGS=-mod=readonly`;
`go list -m -f '{{.Dir}}' github.com/bszymi/spine` prints this worktree. Final run on Spine
`ccfe4ea4`; the same gate also passed on `be492156` (before the codex rounds), `7ce1bbee` and
`38b15f36`.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `bash dev/lint-spine-internals.sh` | OK; `lint-spine-internals: OK` |
| `go test -race -p 4 -count=1` on every SMP package importing a changed Spine package (`go list -test`: `internal/customer/{eventdelivery,projectionsvc,projectionwriter,runner,spinecore,spineembedded,stepevents,workspace}`) plus `./internal/engine/... ./internal/automation/` (the design's prototype set) | all ok (`projectionsvc` has no tests) |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s008_smp_it` with `vector`, `s008_spine_runtime_it`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -v -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 25m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov`, as in CI) | 1914 passed, 0 failed, 2 allowed skips (the Linux-only `ru_maxrss` tests; macOS host), `testgate: PASS` |
| The four files AC 19 names, from that run's `-v` output | `proxy_cancelrun_integration_test.go` 1/1, `stale_cancel_integration_test.go` 2/2, `submit_run_gate_integration_test.go` 3/3, `submitstep_embedded_test.go` 23/23 tests PASS, **0 skipped** |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -p 4 -tags integration ./internal/customer/githttp/` | 301 passed, 0 failed, 1 allowed Linux-only skip, `testgate: PASS` |
| The three Linux-only skips, in `golang:1.26-bookworm` (git 2.39.5, read-only mounts): `TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch` (`spinecore`), `TestIntegration_ReceiveTimeMemoryBounded` (`githttp`, `-tags integration`) | all PASS |

Behaviour at the bump with no SMP change, as designed: run-status writes are compare-and-swap
through SMP's existing `TransitionRunStatus` (a cancel that loses to a merge start answers SMP's
existing 409 mapping); step writes stay blind (SMP's store has neither optional port);
`IngestResult`'s snapshot handoff is active (SMP's `lockExec` serialises submits in-process, so it
does not fire there); `step_completed` carries a payload and a per-execution id, with
`branch_change` `unknown`/`head_sha_unrecorded` (binding off) and no git call; `ReleaseStep`'s owner
guard holds for SMP's three callers (they pass the owner). Environment note: snapshots of the
cluster's `ws_*` databases taken around the SMP runs moved (0 → 2 transient once, 79 → 0 once)
because sibling worktrees ran SMP suites concurrently; the five SMP packages run here contain no
`DROP DATABASE`, and nothing of this task's was left.

### Not run

- Spine's GitHub CI and SMP's CI (nothing was pushed); the jobs were reproduced locally as above.

### Review round 1

Three reviewers (correctness + compatibility, adversarial security, completeness/tests) examined
the branch at `5c197efd`. Every finding was verified against the code first. Fixes are on the
branch head `5921c180`; the mutation overlays named below are under the session scratchpad
(`r1mut/`), applied with `go test -overlay` so the worktree was never touched.

**Findings**

| # | Finding | Disposition |
|---|---|---|
| 1, 6 | P3 — `requiredOutputsUnchanged` logged the `required outputs declared but run branch unchanged` Warn while deriving the flag, before the auto-acknowledge, the `commit_meta` write and the terminal compare-and-swap (`step.go:447`), so a refused or losing submit left an audit warning for an execution it never completed. | **Fixed** `5b7ce6d6`. The derivation is side-effect free (`requiredOutputsUnchanged(stepDef, bc)`); `warnRequiredOutputsUnchanged` runs only after `writeStepExecution` applied, next to `emitStepCompleted`. The evidence key still rides the binding write before the terminal write, as the OQ5 ruling and b911c04 require (Deviation 3 amended). `TestStepCompleted_WarnOnlyAfterTheSubmitApplied`: lost to the timeout scanner (the losing submit's flagged key is stored, no Warn), a failed `commit_meta` write (no Warn), applied (one Warn). Mutant r31 (Warn back before the write) is killed. |
| 2 | P3 — with the step capability, `AcknowledgeStep` answered 409 when it lost its compare-and-swap to the same actor's identical transition, where the sequential retry gets 200 `in_progress`. | **Fixed** `e190b4af`. `lostAcknowledgeRace`: a re-read `(in_progress, req.ActorID)` is judged like a fresh call (`acknowledgeAnswer`: TASK-007's run check, then the idempotent result) and emits no second `step_started`. Every other move keeps the conflict, so the existing released-row case (AC 10) is unchanged. A blind store never misses, so SMP is unaffected. `TestAcknowledgeStep_LostRaceToTheSameActor` (concurrent duplicate → idempotent; a run cancelled meanwhile under `EnforceRunStatus` → TASK-007's refusal; lost to a completion → conflict). Mutants r34 (race always a conflict) and r35 (no run check on the race path) are killed. |
| 3 | P3 — upgrade window: an execution completed by a pre-TASK-008 binary was logged under the per-step `step_completed` id, so recovering it after the upgrade re-emits under the per-execution id and is not deduplicated. | **Real; documented**, not coded (`5b7ce6d6`, `f1614a9c`): the `stepCompletedEventID` comment, `event-schemas.md` §4.2's upgrade note, KNOWN-LIMITATIONS "Engine State Writes …" 7 and the CHANGELOG. The suggested alternative (skip the re-emission when `CompletedAt` predates the deployment) is rejected: startup recovery only ever handles executions completed before the process started, so it would drop the event of every completion a crash interrupted before emission — the case the re-emission exists for. Telling a pre-upgrade execution apart needs a schema change (cross-repo rule 1). |
| 4 | P2 — an empty commit (or a change and its revert) moved the head, the diff succeeded with 0 entries, and the step was recorded `changed`, `changed_paths 0`, unflagged and unwarned. | **Fixed** `5b7ce6d6`. A diff that succeeds with no entry is `unchanged`, `changed_paths 0`, `reason: head_moved_tree_unchanged` (`engine.EvidenceReasonTreeUnchanged`), so it is flagged, persisted flagged and warned. The status is judged on the content, as the flag's meaning ("provably unchanged") requires; the SHAs and the reason keep the move visible. AC 14 (j) (fake `Diff`, 0 entries) and `output_evidence_git_test.go::TestStepOutputEvidence_EmptyCommitIsFlagged` on the real `git.CLIClient` (empty commit; change + revert; a real change stays `changed`, 1 path, unflagged; also green on git 2.39.5). Mutant r30 (zero-entry branch dropped) fails both. Docs: `event-schemas.md` §4.2, `api/spec.yaml`, KNOWN-LIMITATIONS "Self-Attested Labels" (a moved head's `unchanged` depends on the diff, so an operator that resolves arguments by branch name may lose it), CHANGELOG. |
| 5 | P3 — the queue consumer (dormant) could route one completion twice in-process: a redelivery refused because the execution was already completed called `RecoverCompletedStep` while the winning delivery sat between its terminal write and its routing (review-1 from recovery, review-2 from `advanceToNextStep`). | **Fixed** `e190b4af`. The consumer counts, per assignment, its deliveries inside `SubmitStepResult`; a refused delivery recovers only when none is (checked after leaving, so of several refused deliveries the last one out recovers, exactly once). Chosen over the two suggestions: never recovering from the consumer reverts codex round 4 (a redelivery after a routing failure); making the live path claim the successor changes `advanceToNextStep`, a shared seam and SMP's path, and needs a new "routed concurrently" answer for the live submit on a primary-key collision. Residual: a winner in another process, or a submit outside the consumer, is not seen (KNOWN-LIMITATIONS 7). `TestConsumer_OverlappingDeliveriesRouteOnce` (a redelivery inside the winner's window → exactly `review-1`; two refused deliveries → the last one recovers once). Mutants r36 (no in-flight check) and r37 (check before leaving, which also fails `TestConsumer_RedeliveryFinishesIncompleteRouting`) are killed. |
| 7 | P3 — `lostSubmitRace`'s TASK-007 run-terminated branch had no test. | **Fixed** (test) `e190b4af`: `TestIngestResult_LostRaceAnswersLikeTheSequentialCall/run terminated by a cancel` (strict policy, CAS store; `CancelRun` lands after `SubmitStepResult`'s run read → TASK-007's 409, not 200 `skipped`; nothing routed or emitted). Mutant r32 is killed. |
| 8 | P3 — `lostSubmitRace`'s ownership re-check had no test. | **Fixed** (test) `e190b4af`: `…/rebound and completed by another actor` (actor-b completes after `SubmitStepResult`'s read → actor-a gets 403 with nothing disclosed, not 200 with actor-b's outcome). Mutant r33 is killed. |
| 9 | P3 — the AC 16 scenario's event-log deletion deleted nothing: the harness wires no event-log writer. | **Fixed** `58bcfd17`. `TestStepOutputEvidence_RecordedAndReadBack` subscribes `spine serve`'s `DeliverySubscriber` (`WriteEventLog` for every routed event), waits (bounded) for the run's three `step_completed` rows, checks their `branch_change` and flag equal run detail's, deletes them (asserting none remain) and requires run detail deep-equal before and after. The engine and gateway tests keep proving that no git call is made. |
| 10 | P3 — `casNoGit` was documented as failing the test on any call but only counted `Diff` calls, and nothing asserted the count. | **Fixed** `58bcfd17`. It reports an unexpected `Diff` with `t.Errorf`, and both engine-backed gateway tests call `requireNoCall()`. Mutant r38 (`StepOutputEvidence` makes a `Diff` call) now fails `TestRunStatus_StepOutputEvidenceFromOrchestrator` too. |
| codex 8 | P2 (dormant) — `recoverBranchStep` moved the branch cursor with a plain `UpdateBranch` of its own snapshot. Of two recoveries finishing one stranded claim, the late one could reopen a branch its successor had completed (`UpdateBranch` writes status and cursor together); the claim's creator had the same stale write. | **Fixed** `5921c180`: new optional `engine.BranchCursorTransitioner` (mirrors `RunCursorTransitioner`); `(*store.PostgresStore).TransitionBranchCursor` writes only `current_step_id`, only while the branch is pending/in_progress and at the expected step; asserted in `serve_wiring.go` (renaming the method breaks `go build ./service/...`). Both recovery cursor moves use `moveBranchCursorIfAt`; a miss is a conflict and nothing is activated. A store without the capability re-reads first (narrowed; KNOWN-LIMITATIONS 9). SMP's `RunStore` holds the PostgreSQL store as a named field, so nothing is promoted, and SMP never calls recovery. `TestRecoverCompletedStep_BranchCursorNeverRewound` (both paths, with and without the capability) and `TestTransitionBranchCursor` (PostgreSQL). Mutants r39 and r40 (a plain `UpdateBranch` in either path) are killed. |

**Acceptance-criteria gaps**

| Gap | Disposition |
|---|---|
| AC 11 ("without editing any existing test") — two TASK-005 tests edited | **Not a defect; AC wording to amend.** Deviation 2: `TestRecoveryResubmitAndAssertionOrdering` and `TestBindingSurvivesAFailureAfterCompletion` pinned `SubmitStepResult` re-routing a completed execution, which §3a / AC 21 remove, and they still guard the TASK-005 property. Proposed wording: "… without editing any existing test other than the TASK-005 tests that pinned the removed re-route". Review round 1 edited no pre-existing test; every test it touched is this task's own. |
| AC 21 mutation clause ("deleting the terminal check fails both") | **Not a defect: it cannot hold by construction.** Through `IngestResult`, `SubmitStepResult` always receives the snapshot (`assigned`/`in_progress`). A row that is terminal at its read can never equal that snapshot, nor be its same-actor acknowledgment, so the handoff refuses first and the terminal guard is redundant on that path. The guard's own test is `TestSubmitStepResult_NeverReroutesCompletedExecution` (M5); deleting both the guard and the handoff fails `TestIngestResult_LostRaceKeepsWinnerOutcome` (Deviation 4). Proposed amendment: "fails the first; deleting it and the snapshot handoff fails the second". |
| AC 21 `RecoverCompletedStep` "ErrConflict when any later execution exists" | **Not a defect; recorded change** (Deviation 6): refusing every later execution stranded a run that crashed between creating the successor and moving the cursor (codex round 1); exactly one waiting successor under the deterministic id is resumed. |
| AC 20 (`make test-scenario` passes) | **Pre-existing, not caused by this branch.** Re-verified twice this round on fresh databases: the branch has 200 top-level PASS / 31 FAIL / 0 SKIP against a `git archive main` export's 198 / 31 / 0, an identical sorted failing set; the named files pass. The three `run_lifecycle_edge_cases_test.go` tests (submit to a cancelled run, double start, idempotent ingest) abort in fixture seeding on both trees, and pass 3/3 on the branch under an overlay that makes their fixture ids numeric. Fixing those fixtures is outside this task (the known-failure list). |
| AC 19 (`-mod=mod`) | **Fixed.** The SMP gate was re-run twice this round with `GOFLAGS=-mod=mod` (the flag the AC names). `go list -mod=mod -m -f '{{.Dir}}' github.com/bszymi/spine` prints this worktree, there is no `vendor/`, and `git status` was clean after every step. |
| AC 9 (the design PoC still fails; SMP's step writes stay blind until SMP TASK-011) | **Not a defect: owner ruling OQ3** (opt-in port; SMP TASK-011 opts in). Until then SMP's `lockExec` serialises submits in-process, and the snapshot handoff and terminal guard cover the read side (Deviation 12). |
| AC 13 (only one interleaving tested; the reverse order leaves a waiting successor on a cancelled run) | **Documented residual, out of scope** (KNOWN-LIMITATIONS "Engine State Writes …" 4; design "Out of scope"). Closing it needs transactional routing. The stray `waiting` execution is inert under TASK-007's run-status entry checks. |
| Design §4 ("after the §3 write is applied") | **Partly fixed:** the Warn and `step_completed` now follow the applied write (finding 1); the evidence key stays in the binding's write before it, per the OQ5 ruling (Deviation 3, amended). |
| Design §3a item 2 (one accepted move) | **Recorded design change** (Deviation 14), kept. Review round 1 applies the same parity to `AcknowledgeStep` (finding 2). |
| AC 16 design-check clause (event-log deletion) | **Fixed** (finding 9). |
| New DB-backed tests not in CI (noted as not an AC breach) | **Fixed** `d88a6f88` (+ `5921c180`): one step at the end of `scenario-git-http`, following TASK-013/014, runs `TestTransitionStepExecution_*`, `TestTransitionRunStatusAt`, `TestTransitionCurrentStep`, `TestTransitionBranchCursor` and the two TASK-008 scenarios. It fails on any skip or a missing PASS, and was run verbatim on a fresh database (7 + 2 PASS). |
| Checked-consistent items (OQ1–OQ5, the AC 7/10/21 greps, ownership before git, PostgreSQL CAS, SMP build) | No action. The AC 10 grep still holds (`writeStepExecution` is the only engine `UpdateStepExecution` caller); the branch cursor moves added this round go through `moveBranchCursorIfAt`. |

**Gates (final code head `5921c180`; only these notes were committed after it)**

Only `s008_r1_*` databases were created (`it`, `it2`, `scen`, `reg`, `scenm`, `regm`, `scen2`, `reg2`,
`ci`, `ci2`, `cireg`, `smp_it`, `spine_rt`, `smp_it2`, `spine_rt2`), all dropped afterwards. No role
was created or dropped. Containers ran from the present `golang:1.26-bookworm` with
`--pull=never --rm`; none was left. Docker Desktop did not mount same-path `/Users/...` targets for
these directories in this session, so the trees were mounted at `/w`, read-only. The tests need
no outer `.git` (`-buildvcs=false`, temporary repositories), and git's identity was written from
`/tmp`, because the worktree's `.git` file names a path the container does not have.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `embed-smoketest` | `lint-boundaries: OK`; OK; "engine constructed and exercised; library surface is sound." |
| `go vet ./...` plain, `-tags integration`, `-tags scenario` | clean |
| `go test -p 4 -count=1 ./...` | 40 packages ok |
| `go test -race -p 4` on `core/engine/...`, `core/actor`, `adapters/store`, `adapters/scheduler`, `service/internal/gateway`, `service/cmd/spine` | all ok, no race |
| `-tags integration -race -p 1` on the same six (`s008_r1_it`, reused) | 2757 PASS, 0 SKIP; FAIL `TestRunModeDatabaseDefault` (known, pre-existing) and `TestEventSubscription_SigningSecretEncryption` (a `duplicate key` on its fixed id `sub-enc-001`, left by this round's earlier run on the reused database; on a fresh `s008_r1_it2` `adapters/store` fails only `TestRunModeDatabaseDefault`) |
| Full scenario suite, branch (fresh `s008_r1_scen2`/`reg2`) vs `main` export | 200 / 31 / 0 vs 198 / 31 / 0, identical failing set; `cancel_from_partially_merged` 1/1, `publish_step` 1/1, `standard_run` 2/2, the two TASK-008 scenarios PASS; edge cases 3/3 under the numeric-id overlay |
| CI `scenario-git-http`, all six steps (incl. the new one), fresh `s008_r1_ci2` | 20 (8 `TestGitHTTP_Push*`) / 2 / 4 / 1 / 7 / 2 PASS, 0 SKIP |
| CI `git-floor` in `golang:1.26-bookworm` (git 2.39.5) + the touched packages | all ok (includes the real-git empty-commit test) |
| `golangci-lint run --new-from-rev main`, plain / integration / scenario | 0 issues (two gocritic findings on the first pass were fixed before commit) |
| `--enable-only=gosec`, `--new-from-rev main` | 0 issues. A full run without `--new-from-rev` shows only `main`'s G602 in `adapters/store/postgres_integration_test.go:1563` (file untouched). |
| `gofmt -l` on changed `.go` files; `git diff --stat main -- migrations/` | empty; empty |
| Mutation overlays r30–r40 (and r41: the store method renamed breaks the build) | every mutant killed |
| SMP gate (`1ae9b0e3`, no SMP change, `GOFLAGS=-mod=mod`) | build, vet (plain / integration / scenario / faultinject), `lint-spine-internals` OK; `-race` on the 8 importers of a changed Spine package + `./internal/engine/... ./internal/automation/` ok; testgate runtime 1914 passed, 0 failed, 2 allowed skips; githttp 301 passed, 1 allowed skip; the four AC 19 files 1/1, 2/2, 3/3, 23/23, 0 skipped; the three Linux-only tests PASS in the container; `ws_*` databases/roles 0 → 0 |
| Codex | rounds 8 and 9 above, both `gpt-6.1-sol` with no override |

### Review round 2

A second-round reviewer examined the branch at `7c54dc82`. It reported **no findings**. It
re-verified every round-1 disposition with its own mutants: every one was killed, and the one
survivor, the store-capability branch of `moveBranchCursorIfAt`, is the window KNOWN-LIMITATIONS 9
documents. It re-ran the gates on `7c54dc82` (all green; the SMP DB-backed gate was not re-run
there). It raised one wording gap. The plain `codex review --base main` gate, run on that tree
(round 10), raised one P2. Both are dispositioned below. The code fix is `1ce429b7` and its
docs are `55d2a366`.

**Gap (wording): the tree-judged evidence was not a numbered deviation**

| Gap | Disposition |
|---|---|
| Review round 1 finding 4 (`5b7ce6d6`: a moved head whose diff has no entry is `unchanged` / `head_moved_tree_unchanged`) contradicted design §4 ("success → `changed` with `len(diffs)`"). AC 14 lists only (a)–(i). The change was recorded only in the Review round 1 table and the AC 14 evidence row. | **Fixed (wording).** Deviation 17 records it, with the reason, the alternative not taken, the tests (AC 14 (j), `TestStepOutputEvidence_EmptyCommitIsFlagged` on the real `git.CLIClient`, `TestStepCompleted_WarnOnlyAfterTheSubmitApplied`) and mutant r30. Design §4 is amended in place: the `Reason` list in the `BranchChange` sketch, and the diff bullet now reads "success with no entry → `unchanged`, `ChangedPaths = 0`, `reason: head_moved_tree_unchanged`", marked as amended. The AC text itself is left to the owner's wording pass. Proposed AC 14 addition: "(j) Head ≠ previous, with the fake `Diff` returning no entry, gives `status:"unchanged"`, `changed_paths:0`, `reason:"head_moved_tree_unchanged"` and `required_outputs_unchanged:true`, and the Warn is logged only after the terminal write applied; on a real repository an empty commit, and a change plus its revert, are flagged the same way." |
| The reviewer's re-verified dispositions (8 FIXED, 3 NOT REAL, the AC 9 / 11 / 13 / 21 items) | No action. The reviewer confirmed them with its own mutants, including the `moveBranchCursorIfAt` fallback survivor that KNOWN-LIMITATIONS 9 documents. |

**Codex round 10 (P2): a lost claim's cancelled row read as a binding**

| Finding | Disposition |
|---|---|
| P2: when `ClaimStep` loses its conditional step write (this task's §3 row), it cancels the `claim-<execution>-<actor>` row it created, and `runAuthor` counts assignment rows in every status. So under separation of duties the losing claimant is refused as a reviewer of a run whose author step it never held. | **Real; fixed** `1ce429b7`, docs `55d2a366` (Deviation 18). Verifying it found two more effects of the same row. TASK-007's `releasedSince` reads a cancelled `claim-<execution>-<holder>` row as "held and released", so a prior holder's lost claim opened its re-offered rework attempt to every other actor. And `assignment_id` is the primary key, so that actor could never claim the execution again. The row is now removed through the new optional `engine.AssignmentRowDeleter` (`(*store.PostgresStore).DeleteAssignment`, guarded on execution and actor, asserted in `serve_wiring.go`, no schema change). Without the port, or on a failed delete, it is cancelled as before (KNOWN-LIMITATIONS "Engine State Writes …" 11). Push admission and `ReleaseStep`'s owner guard key on the execution, not on the row, so the removal erases no right that was ever exercised. Tests: `TestClaimStep_LostRaceLeavesNoLedgerRow` (SoD: the lost claimant claims the review while the executing actor is refused; re-offer: another actor is refused and the holder's next claim binds; no port: cancelled and still refused, the residual; failing delete: cancelled, never active) and `TestDeleteAssignment` on PostgreSQL (a wrong actor or execution deletes nothing; deleting nothing is nil; the id can be created again; a cancelled row goes too), the latter added to CI's TASK-008 step. The AC 10 table row asserts the removal. SMP is unaffected: the path needs `engine.StepExecutionTransitioner`, and SMP's step writes are blind until SMP TASK-011, whose handoff now includes the deleter. |

Mutation overlays (`go test -overlay`, worktree untouched), all killed. r42 always cancels (the
round-1 code): it fails the SoD and re-offer sub-tests and the AC 10 table row. r43 drops the
cancel fallback: it fails the no-port and failing-delete sub-tests. r44 makes `DeleteAssignment`
cancel instead of delete, and r45 drops its execution/actor guard: both fail
`TestDeleteAssignment`. The design §3 `ClaimStep` row is amended in place (marked). Proposed AC 10
wording: "the assignment row is removed on the `ClaimStep` miss (cancelled when the assignment
store lacks `engine.AssignmentRowDeleter`)". Docs: CHANGELOG (security bullet and "Changed
(embedders)"), KNOWN-LIMITATIONS "Engine State Writes …" 11, `engine-state-machine.md`
"Concurrent writes", `actor-model.md` §7.3, `docs/integration-guide.md` §10.

**Codex**

| Round | Tree | Review session | Result |
|---|---|---|---|
| 10 | `7c54dc82` (+ the uncommitted design §4 / Deviation 17 wording) | `01a13c92-c808-79b3-8846-53a94cdd8309` | P2: the lost claim's row (above). **Fixed.** |
| 11 | the fix, uncommitted, on `7c54dc82` | `01a13ca2-88e0-7c70-9b12-724f29e5cc5f` | "No actionable regressions were identified in the diff." |
| 12 | `55d2a366` | `01a13caf-bd15-7870-85dc-2c3b22bbd399` | "No actionable regressions were identified in the changes." |

Every round ran `codex review --base main` plain, with no `-c model=` or other override. Each
review sub-session (`session_meta.source` = `{"subagent":"review"}`, under
`~/.codex/sessions/2026/10/14/` and `…/15/`) records `"model":"gpt-6.1-sol"`.

**Gates (final code head `1ce429b7`, docs `55d2a366`; only these notes were committed after them)**

The gates ran on the working tree that was then committed unchanged as these two commits;
codex round 12 reviewed the committed head. Only `s008_r2_*` databases were created (`it`,
`it2`, `ci`, `scen`, `reg`, `clm`, `smp_it`, `spine_rt`); all were dropped afterwards. No role
was created or dropped. Containers ran from the present `golang:1.26-bookworm` with
`--pull=never --rm`, with the trees mounted read-only at `/w`; none was left.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `embed-smoketest` | `lint-boundaries: OK`; OK; "engine constructed and exercised; library surface is sound." |
| `go vet ./...` plain, `-tags integration`, `-tags scenario` | clean |
| `go test -p 4 -count=1 ./...` | 40 packages ok, no failure |
| `go test -race -p 4` on `core/engine/...`, `core/actor`, `adapters/store`, `adapters/scheduler`, `service/internal/gateway`, `service/cmd/spine` | all ok, no race |
| `-tags integration -race -p 1` on the same six (fresh `s008_r2_it2`) | 2764 PASS, 0 SKIP; 1 FAIL: `TestRunModeDatabaseDefault` (known, pre-existing) |
| CI `scenario-git-http`, all five steps scripted verbatim from `ci.yml` (fresh `s008_r2_ci`) | all exit 0: `TestGitHTTP_*` 20 PASS (8 `TestGitHTTP_Push*`); `TestBootstrapInternalAdmin_*` 2; `TestWorkflowLifecycle_*` 4; `TestBuildServiceSet_WorkflowServiceHasProjectionPolicy` 1; TASK-008 step 8 store tests (incl. `TestDeleteAssignment`) + 2 scenarios. 0 SKIP |
| Full scenario suite (fresh `s008_r2_scen` / `s008_r2_reg`, registry migrated) | 200 PASS / 31 FAIL / 0 SKIP. The failing set is identical to round 1's (the known pre-existing 30 + `TestWorkspace_ScopedServices`). `TestClaiming_ClaimAndRelease`, the claim/release scenario that this round's change touches, is one of the 31: its fixture ids (`INIT-CLM`) fail seeding on both trees. Under an `-overlay` with numeric ids it passes all 15 steps on PostgreSQL (`s008_r2_clm`). `TestStepOutputEvidence_RecordedAndReadBack`, `TestCompletedStep_IsNotReRouted`, `cancel_from_partially_merged`, `publish_step` and `standard_run` PASS |
| CI `git-floor` steps in `golang:1.26-bookworm` (git 2.39.5, `GOPROXY=off`), plus `./core/engine/ ./adapters/store/ ./service/cmd/spine/` | all ok |
| `golangci-lint run --new-from-rev main` per touched package, plain and `--build-tags=integration`; `--build-tags=scenario ./service/scenariotest/...` | 0 issues each |
| `--enable-only=gosec --build-tags=integration,scenario`, `--new-from-rev main` | 0 issues |
| `gofmt -l` on every changed `.go` file; `git diff --stat main -- migrations/` | empty; empty |
| AC 7 / AC 10 greps | `store.UpdateRunStatus(` prints nothing; `store.UpdateStepExecution(` prints only `state_cas.go:288`, inside `writeStepExecution` |
| Mutation overlays r42–r45 | every mutant killed |
| SMP gate (`1ae9b0e3`, no SMP change, `git status` clean after every step, `GOFLAGS=-mod=mod`; `go list -m` resolves Spine to this worktree) | build; vet plain / `integration` / `scenario` / `faultinject`; `lint-spine-internals` OK. `-race -p 4` on the 8 direct importers of `core/engine` or `adapters/store` (`internal/customer/{eventdelivery,projectionsvc,projectionwriter,runner,spinecore,spineembedded,stepevents,workspace}`) + `./internal/engine/... ./internal/automation/`: all ok. Platform / spine-runtime migrations 116 / 26 versions (fresh `s008_r2_smp_it` with `vector`, `s008_r2_spine_rt`). Testgate runtime suites: 1914 passed, 0 failed, 2 allowed skips (Linux-only `ru_maxrss`); githttp `-tags integration`: 301 passed, 0 failed, 1 allowed skip. The four AC 19 files: 1/1, 2/2, 3/3, 23/23, 0 skipped. The three Linux-only tests (`TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch`, `TestIntegration_ReceiveTimeMemoryBounded`) PASS in the container. `ws_*` databases and roles 0 → 0 |

### Handoffs

- **SMP TASK-011:** pass `SubmitRequest.ActorID` on the member and runner paths and carry the
  claimed handle through the agent-engine seam (design §8; the adapter has no actor today); opt
  into the step compare-and-swap with a `TransitionStepExecution` delegate to
  `(*store.PostgresStore)` plus `var _ engine.StepExecutionTransitioner = (*RunStore)(nil)` (keep
  `execlock.go`); with it `ReleaseStep` switches to step-first ordering with compensation. Expose
  `engine.AssignmentRowDeleter` on the assignment store at the same time (a `DeleteAssignment`
  delegate to `(*store.PostgresStore)` plus `var _ engine.AssignmentRowDeleter = …`). Otherwise a
  lost claim leaves its row cancelled, and separation of duties and the prior-holder re-offer read
  it as a binding (review round 2, Deviation 18). Map
  `engine.ErrStepStateChanged` / `ErrRunStateChanged` (both `ErrConflict`); SMP does not call
  `RecoverCompletedStep`, so `engine.RunCursorTransitioner` and `engine.BranchCursorTransitioner`
  are optional for it. With the step capability, `AcknowledgeStep` answers a race lost to the same
  actor's acknowledgment with the idempotent `in_progress` result (review round 1). Re-run this
  gate.
- **SMP TASK-009 → TASK-011 item 3:** evidence needs merge binding; then surface
  `Orchestrator.StepOutputEvidence` on run detail and audit `required_outputs_unchanged`.
- **Owner question 4 follow-up:** fail closed on an empty `SubmitRequest.ActorID` (an Orchestrator
  option plus a system-submit marker) once SMP TASK-011 sends it everywhere.
- **Integration:** this task's shared seams are `core/engine/step.go` (`SubmitStepResult`,
  `RecoverCompletedStep`, `recoverBranchStep`), `result.go`, `run.go`, `merge.go`
  (`failRunOnMergeError`), `handlers.go`, `commitmeta.go` (`headBinding.extra`),
  `acknowledge.go`, `consumer.go`, `adapters/store/postgres_divergence.go`
  (`TransitionBranchCursor`), `adapters/store/postgres_assignments.go` (`DeleteAssignment`),
  `core/engine/claim.go` (one call to `discardLostClaimRow`), the end of CI's `scenario-git-http`
  job, and the `CHANGELOG.md` top section.
- **Owner / design:** amend the wording of AC 11 and AC 21 as proposed in "Review round 1", and
  of AC 10 and AC 14 as proposed in "Review round 2" (Deviations 18 and 17). The three `run_lifecycle_edge_cases_test.go` fixtures (`INIT-EC*` ids) are a known pre-existing
  failure; fixing them would restore that coverage in `make test-scenario`.

### Integration (wave 4 merge) (2026-10-19)

The four wave 4 branches were merged with `git merge --no-ff` onto `init/INIT-023-wave4-integration`
(worktree `i23/s-int4/spine`, from Spine local `main` `ae513dca`), in this order: TASK-012
(`3872f4d6`), TASK-008 (`1fef1444`), TASK-017 (`2cbfe037`), TASK-006 (`0b9a4f22`). The semantic
conflicts between them were then fixed in coordination commits, each code fix with a test that
fails without it: `3ea7d88f` (engine), `4dbf0486` (serve wiring test and docs), `96cf8e40` (docs),
`044c2103` (lint, test code) and `a6acc720` (codex round 2). The final code is `a6acc720`; the
commit that adds this note changes only the four task artifacts. Nothing was pushed. Spine
`main`, the task branches and the SMP checkout are unchanged. The owner questions of all four
tasks stay recorded as defaults, pending the owner; none was re-decided.

**This task.** It merged second. The only conflict was in `CHANGELOG.md`.

- **AC 10 holds again on the merged code.** TASK-006 had added two blind step writes: the hold
  record (`recordGovernanceHold`) and its clearing on approval (`clearGovernanceHold`). Both now go
  through `writeStepExecution`. `grep -n 'store.UpdateStepExecution(' core/engine/*.go`
  (non-test) prints only the line inside `writeStepExecution` (`state_cas.go:288`).
  `TestWave4_GovernanceStepWritesAreConditional` uses a store with the capability whose row moves
  before the write. A stale copy is never written back, and the mutants that restore either blind
  write are killed.
- **AC 7 holds.** `grep -n 'store.UpdateRunStatus(' core/engine/*.go` (non-test) prints nothing.
- **The run and step compare-and-swap covers the other tasks' routing writes.**
  - TASK-012's exhaustion route: `handlePermanentMergeFailure` leads to `failRunOnMergeError` or
    to `advancePublishStepIfAny`, and `recordGitConflictOnStep` is conditional too.
  - TASK-006's reject: a conditional `TransitionRunStatus` on the no-publish route (it keeps its
    own because it must answer 409); `advancePublishStepIfAny`, `recordGitConflictOnStep` and
    `terminateLiveSteps` otherwise. The stale "blind until TASK-008" comment there is corrected.
  - TASK-006's approve: `MergeCommitMetaIfStatus`.
- **`commit_meta` writers audit.** Every engine writer of the merged code merges keys and never
  replaces the map blindly:
  - the binding write with this task's evidence keys (`mergeCommitMeta`, through
    `headBinding.extra`);
  - the convergence and recovery-ledger rebinds (`withEngineBinding`);
  - TASK-006's `MergeCommitMeta` / `MergeCommitMetaIfStatus` (the approval, the tombstone, the
    marker, `started_by`, `blocked_target`), with a read-modify-write fallback for stores
    without them.

  TASK-012 writes no `commit_meta`: its publication-pending carry is in the outcome's failure
  detail. The read-modify-write paths keep their documented cross-process residual
  (KNOWN-LIMITATIONS "Governance-Path Guard" 9).
- **CI.** This task's CI step is present once, and passes in the CI job run below: 8 store tests
  and 2 scenarios, 0 skips.
- **Integration guide.** It keeps §10. TASK-017's and TASK-006's sections are now §11 and §12.

**Coordination commits.**

| Commit | What it fixes | Tests |
|---|---|---|
| `3ea7d88f` | V1 is taken back to exactly `BaseSHA`; a failed take-back of a TASK-005 or V1 refusal records the governance block (guard on); the retry budget spares held and blocked runs; the lifecycle's transient exit is a counted attempt; the governance hold and its clearing are compare-and-swap; the decision API mirrors the lifecycle | `core/engine/wave4_integration_test.go` (9 tests), `wave4_integration_git_test.go` (4 real-git cases). Every fix has a mutant killed under `go test -overlay`: M1–M7, M3b, and M8/M9 (governance check, V1 moved out of the block). The overlays and logs are in `w4/agents/int-spine/mut/` |
| `4dbf0486` | Serve wiring composition; docs: the order of checks (`engine-state-machine.md`), the merge block (`git-integration.md` §6.6), KNOWN-LIMITATIONS, integration guide §11/§12, CHANGELOG "wave 4 integration" | `service/cmd/spine/serve_wave4_wiring_test.go`: both builders have binding, lifecycle, guard, home-branch primary client (a `MergeBaseLister`), retry budget and handler, governance-aware retry, projection guard |
| `96cf8e40` | ADR-016 and KNOWN-LIMITATIONS: convergence is no longer a path around the guard | docs only |
| `044c2103` | gocritic and staticcheck on the new test | test code only |
| `a6acc720` | Codex round 2 P2: `MergeBases` resolves `HEAD` to the home branch | `adapters/git/wave4_mergebases_test.go` (mutant M10 killed) |

**Gates.** The code gates ran on `a6acc720` unless the row names `96cf8e40`. The only code
change after `96cf8e40` is `a6acc720`'s `HEAD` rewrite in `MergeBases`, which no engine or
scenario caller reaches (they pass `main` or object ids). Logs are in
`spine-management-platform/tmp/init027-orchestration/w4/agents/int-spine/`.

| Gate | Result |
|---|---|
| `./dev/lint-boundaries.sh`; `make build`; `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` | OK; OK; "engine constructed and exercised; library surface is sound" |
| `go vet ./...`, `-tags integration`, `-tags scenario` | clean, all three |
| `go test -p 4 -count=1 ./...` | 40 packages ok, 0 failures |
| `go test -race -p 4 -count=1` on the 19 packages changed since `ae513dca` (`service/scenariotest/scenarios` builds only with `-tags scenario`; the scenario rows cover it) | 19 ok, 0 data races (`adapters/{delivery,git,projection,scheduler,store}`, `core/{actor,artifact,auth,branchprotect,divergence,domain,engine,validation,workflow,worktreefs}`, `service/cmd/spine`, `service/internal/{gateway,workspace}`, `service/scenariotest/harness`) |
| `-tags integration -p 1 -count=1 -v` on the changed packages with integration suites: `adapters/{delivery,projection,store}`, `service/cmd/spine`, `service/internal/workspace`. `SPINE_TEST_DATABASE_URL` = fresh `s4i_it`, `SPINE_REGISTRY_TEST_DATABASE_URL` = `s4i_reg`, migrated from `migrations/registry` | 584 top-level PASS (1161 with subtests), 0 SKIP, 1 FAIL. The failure is `adapters/store` `TestRunModeDatabaseDefault` (`cannot scan NULL into *string`, column `workflow_version_label`), the known pre-existing failure: a `git archive` export of `main` `ae513dca` fails it the same way. Run at `96cf8e40` and again at `a6acc720`, with the same result |
| Full scenario suite (`-tags scenario -p 1 -count=1 -v ./service/scenariotest/...`), branch at `96cf8e40` against a `git archive` export of `main` `ae513dca`; fresh databases each, registry migrated | 31 top-level `--- FAIL` on both, and the sorted lists are identical, at every level. They are the known pre-existing set: the 30 plus `TestWorkspace_ScopedServices`, the same list as wave 3. Top-level PASS is 204 on the branch and 198 on `main`. The extra 6 are the wave's scenarios: `TestCompletedStep_IsNotReRouted`, `TestStepOutputEvidence_RecordedAndReadBack`, `TestGovernancePathGuard_HeldUntilOperatorApproves` and the three `StatusLifecycle` scenarios. 0 SKIP |
| CI `scenario-git-http`: its 7 `run:` steps, read from `ci.yml` and run verbatim (`bash --noprofile --norc -eo pipefail`, `SPINE_TEST_DATABASE_URL` = fresh `s4i_ci`) | All 7 steps exit 0. `TestGitHTTP_*`: 20 PASS. `TestBootstrapInternalAdmin_*`: 2. `TestWorkflowLifecycle_*`: 4. Pooled workflow-service branch protection: 1. TASK-008's store compare-and-swap: 8, and its scenarios: 2. TASK-017's `StatusLifecycle` scenarios: 3, and its pooled validator: 1. TASK-006's `TestGovernancePathGuard_HeldUntilOperatorApproves`: 1, and `TestGovernanceStore_Integration`: 1. 0 SKIP and 0 FAIL in every step |
| CI `git-floor`: its 3 `run:` steps in `golang:1.26-bookworm` (`--pull=never --rm --init`, non-root, read-only mounts, `GOPROXY=off`, no git identity); plus an extra run of `core/engine` `-run 'TestWave4_\|TestStatusLifecycle_\|TestGovernanceGuard_\|TestF9_\|TestMergeRetry_'` | git 2.39.5. All 3 steps ok: `adapters/git/...` and `core/engine` (ref-grammar filter), then `adapters/git/...`, `core/artifact` and `core/workflow`. The extra `core/engine` run is ok on git 2.39.5 too, both at `a6acc720` |
| `golangci-lint` v2.11.4 `run --new-from-rev ae513dca` on the changed packages, plain, `--build-tags integration` and `--build-tags scenario` | 0 issues each. The two issues in the new test at `96cf8e40` were fixed in `044c2103` |
| `golangci-lint --enable-only=gosec`: `--new-from-rev ae513dca ./...`, full `./...`, and full `--build-tags integration,scenario` | 0; 0; 1. The one is G602 at `adapters/store/postgres_integration_test.go:1563`, a file unchanged since `main` (wave 3 recorded the same finding) |
| `gofmt -l` on the 167 `.go` files changed since `ae513dca` | only `core/divergence/convergence.go`: the import-order problem it has on `main` `ae513dca`, covered by TASK-012's AC 23 exemption |
| `ci.yml` | one step per task: TASK-008's, TASK-017's and TASK-006's in `scenario-git-http`; none duplicated; the YAML parses |
| `codex review --base main` (codex-cli 0.162.1, the configured default, no `-c model=`; sessions read from `~/.codex/sessions/2026/10/19/`, `source: {"subagent":"review"}`, `cwd` = this worktree, `model` = `gpt-6.1-sol`) | Round 1 at `96cf8e40`: `01a153e5-817e-7f33-9a6b-680376b6f857`, no findings. Round 2 at `044c2103`: `01a153fc-abf2-7a72-9d81-03a7e51ef284`, one P2 (the `MergeBases` `HEAD` rewrite): real, reproduced, fixed in `a6acc720`. Round 3 at `a6acc720`: `01a15409-966a-7da1-a64f-248cfbd7f2b4`, no findings |

**SMP compatibility.** `i23/s-int4/smp` is SMP `main` `1ae9b0e3`, detached, with `../spine` =
this worktree and `GOFLAGS=-mod=readonly`. The checkout was never modified, and `git status` is
clean.

| Command | Result |
|---|---|
| `go build ./...`; `go vet ./...` plain, `-tags integration`, `-tags scenario`, `-tags faultinject`; `dev/lint-spine-internals.sh` | all OK; `lint-spine-internals: OK`; `git status` clean |
| `go test -race -p 4 -count=1`, no DSNs, on the 14 SMP packages importing a changed Spine package (test imports included): `cmd/customer-app`, `internal/customer/{eventdelivery,executionreadiness,githttp,gitread,projectionsvc,projectionwriter,proposals,runner,spinecore,spineembedded,stepevents,workspace}`, `internal/recovery` | 13 ok; `projectionsvc` has no tests. No failures: the wave 3 `gitread` / `projectionwriter` contract failures are gone on SMP `main` `1ae9b0e3` |
| `go run ./dev/apply-migrations -set platform -dir migrations` / `-set spine-runtime` (fresh `s4i_smp_it` with `vector`, `s4i_spine_runtime_it`) | 116 / 26 versions recorded |
| SMP CI "Spine runtime integration suites": `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 20m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/` (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN` = `smp_prov`, as in CI) | `4 packages, 1914 tests passed, 0 failed, 2 skipped (2 allowed)`; `testgate: PASS` |
| SMP CI "Git gateway integration tests": `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/` | `1 packages, 301 tests passed, 0 failed, 1 skipped (1 allowed)`; `testgate: PASS` |
| The three Linux-only tests those allowances skip on macOS, in `golang:1.26-bookworm` (git 2.39.5): `TestDiffChildMemory_MillionPaths`, `TestReceiveTimeMemory_RealGitReaderFetch` (`spinecore`) and `TestIntegration_ReceiveTimeMemoryBounded` (`-tags integration`, `githttp`) | all 3 PASS on git 2.39.5; the containers were removed |

No SMP failure, so no compatibility break.

**Not run.**
- GitHub CI: it cannot run this branch until Spine is pushed.
- The other steps of SMP's integration job (platform-DB shared suites, fenced migrate,
  workspace-schema, faultinject, verify-children) and the image-based gates. They are not in this
  gate list.
- The full scenario suite was not repeated after `a6acc720`, for the reason in the Gates
  preamble.

**Environment.**
- PostgreSQL: the shared `i23-pg` on `localhost:55630`. Only `s4i_*` databases were created, and
  all were dropped. There were no `ws_*` databases or roles before the SMP suites and none
  after, and the shared roles were untouched.
- Docker: the only containers were the `git-floor` runs, the Linux-only SMP runs and the
  `alpine:3 df` checks, all `--rm`. Nothing was pulled or pruned.
