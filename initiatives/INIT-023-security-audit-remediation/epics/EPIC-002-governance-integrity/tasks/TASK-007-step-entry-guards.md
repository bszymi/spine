---
id: TASK-007
type: Task
title: "Opt-in step-entry policy for assign and claim: run state, eligibility, mode, preconditions, separation of duties"
status: Pending
epic: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
initiative: /initiatives/INIT-023-security-audit-remediation/initiative.md
work_type: security
created: 2026-10-06
last_updated: 2026-10-06
links:
  - type: parent
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/epic.md
  - type: related_to
    target: /architecture/engine-state-machine.md
  - type: related_to
    target: /architecture/workflow-definition-format.md
  - type: related_to
    target: /architecture/adr/ADR-009-branch-protection.md
  - type: related_to
    target: /initiatives/INIT-010-actor-skill-execution/epics/EPIC-001-skill-system/tasks/TASK-004-skill-eligibility-validation.md
  - type: related_to
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-004-auto-assigned-step-with-null-actor.md
---

# TASK-007 — Opt-in step-entry policy for assign and claim: run state, eligibility, mode, preconditions, separation of duties

---

## Purpose

This task closes four P3 findings from the 2026-10-06 security audit:

- **P3-3** (Spine side). `AssignStep` checks no actor constraints.
- **P3-4** (Spine side). A step whose preconditions failed can still be assigned or claimed, and two of the engine's precondition checks fail open.
- **P3-15**. `ClaimStep` ignores `execution.mode`.
- **P3-16**. Engine step entry points never check run status, and `CancelRun` leaves step executions live.

It also adds the separation-of-duties rule. The audit names that rule as the amplifier fix for F3: "enforce separation of duties in `AssignStep`" (F3 one-line fix). The F3 merge-path guard itself belongs to TASK-006.

The audit ran against Spine `61994fe` and SMP `80995559`. Line numbers below refer to Spine `61994fe` unless they are marked SMP. "SMP" means `spine-management-platform` at `80995559`, which embeds Spine `61994fe` (`.spine-revision`).

### The defects

1. **`AssignStep` binds any string to any step (P3-3).**
   - `Orchestrator.AssignStep` (`core/engine/assign.go:32-75`) validates the parameters (`:33-38`) and finds the execution (`:40`). It then runs the state-machine transition (`:56-64`) and writes `exec.ActorID = req.ActorID` (`:66-72`).
   - It never loads the run, the workflow or the actor. The doc comment says so: "Precondition evaluation is the caller's responsibility" (`:29-31`).
   - So it accepts actor IDs that do not exist, actors of the wrong type (an `ai_agent` on a `human_only` step), and actors outside the execution's own `EligibleActorIDs` allow-list. It also accepts actors without the step's `required_skills`.
   - The spec says the opposite. `engine-state-machine.md` §3.2 lists the guard for `waiting → assigned / step.assign` as "Eligible actor found; skill eligibility validated".
   - `ClaimStep` already enforces the allow-list (`core/engine/claim.go:79-91`), the actor type (`:100-113`) and the skills (`:116-142`). The two binding paths therefore disagree.
2. **No separation of duties (F3 amplifier).**
   - Neither path stops the actor who executed a run's work from being bound to that run's review step.
   - `poc/skeptic-selfreview` binds `member-1`, the executor of `execute`, to `review` through `AssignStep` with no workflow loaded at all.
3. **Preconditions run only at activation (P3-4).**
   - `ActivateStep` evaluates preconditions (`core/engine/step.go:59-61`). On failure it leaves the step `waiting`, because `waiting → blocked` is not a valid transition (`:133-157`).
   - `StartRun` swallows that failure (`core/engine/run.go:117-119`) and so does `advanceToNextStep` (`step.go:446-448`).
   - Nothing re-evaluates preconditions when the step is later claimed (`claim.go:31-198`) or assigned (`assign.go:32-75`).
   - Callers use `validation.EvaluatePreconditions` (`core/validation/precondition.go:17-64`). It skips every type except `cross_artifact_valid` (`:26-29`), so `artifact_status` and `discussions_resolved` never block a bind.
   - The engine's own evaluator (`step.go:724-789`) also fails open in three places:
     - unknown precondition types are skipped (`:783-785`);
     - `cross_artifact_valid` is skipped when no validator is wired (`:759-762`);
     - `discussions_resolved` passes when no checker is wired (`:875-878`).
   - It also has a latent correctness bug. `field_value` and `field_present` read `art.Metadata[field]` (`:845`, `:854`). The parser deletes `id`, `type`, `title` and `status` from the metadata map and never puts them back (`core/artifact/parser.go:75-78`).
   - So `field_value: {field: status, ...}` can never pass. `poc/skeptic-precond` shows that `art.Metadata["status"]` is absent for a task whose status is `In Progress`.
   - `workflow-definition-format.md` §5.2 defines `field_value` as "Specified front matter field must have the given value", and `status` is a front-matter field.
4. **`ClaimStep` ignores `execution.mode` (P3-15).**
   - An engine-owned `spine_only` publish step (`type: internal`) left `waiting` can be claimed by any actor. A `published` outcome then completes the run with no merge.
   - `poc/engine-authz/poc_spine_only_claim_test.go` shows `run status=completed, merges performed=0`.
5. **Run status is never checked, and cancel leaves steps live (P3-16).**
   - `ClaimStep` loads the run (`claim.go:60`) but never looks at `run.Status`. `AssignStep` never loads it.
   - `AcknowledgeStep` (`core/engine/acknowledge.go:29-87`), `IngestResult` (`core/engine/result.go:21-139`), `SubmitStepResult` (`step.go:283-356`), `ActivateStep` (`step.go:35`) and `RetryStep` (`core/engine/retry.go:17`) do not check it either.
   - `CancelRun` (`run.go:486-516`) only flips the run row and deletes the branch. `FailRun` (`run.go:452-483`) and `failRunOnMergeError` (`core/engine/merge.go:443-466`) behave the same way.
   - A cancelled run's `waiting`, `assigned` or `in_progress` executions therefore stay bindable and submittable.
   - Submitting one advances the run's steps. `advanceToNextStep` (`step.go:412-450`) creates the next execution and calls `ActivateStep`. For an `automated_only` or `ai_only` step that auto-selects an actor and calls `DeliverAssignment` (`step.go:73-111`), which dispatches runner work for a cancelled run.
   - `engine-state-machine.md` §2.2 already requires "Terminate in-progress steps" on `active → cancelled / run.cancel`. The code does not do it.

### Who can exploit this, and the impact

| Finding | Attacker | Standalone `spine serve` | Embedded SMP |
|---|---|---|---|
| P3-3 | **A2**: an operator, because `step.assign` is `RoleOperator` (`core/auth/permissions.go:34`) | Binds anyone (an `ai_agent`, a skill-less contributor, a non-existent id) to a review step. A review gate defined in the workflow can be satisfied by an actor the workflow excludes. | `run.step.assign` is open to every member and defaults the assignee to the caller (SMP `internal/customer/run/handler.go:653-690`). For `hybrid`, `human_only` and unset modes the SMP gate forwards to Spine's ungated `AssignStep` (SMP `internal/customer/spineembedded/proxy.go:1024-1045`). Within member authority, but there is no type, eligibility or separation-of-duties check. |
| SoD | **A2**: a member who executed the run | Self-assigns review and approves their own work, which then reaches `main` through the publish step (`poc/governance/assign_bypass_test.go`) | Same, through `POST /runs/{run}/steps/review/assign` (`poc/verify-r1-6`). This is the F3 amplifier: one member satisfies the whole chain alone. |
| P3-4 | **A2** | A task still `Draft` on `main` is executed and reviewed through assign (`poc/verify-r1-7`) | Same funnel (`proxy.go:1032` evaluates only `cross_artifact_valid`) |
| P3-15 | **A2** with `execution.claim` (`RoleContributor`, `permissions.go:47`) | Claims an engine-owned publish step and completes the run without a merge | Guarded today by SMP's `claimModeGate` (SMP `executor_gates.go:132-153`) and by the assign mode switch (SMP `proxy.go:1024-1031`) |
| P3-16 | **A2/A6**: any actor still bound to a cancelled run, or the run's runner | Advances a cancelled run and dispatches runner jobs for it | SMP checks run status on its runner paths (`planRunAndMode`, SMP `automation_recovery.go:164-179`), but not on member assign or claim |

Severity is P3: these are integrity gaps inside authenticated authority. The SoD part is the reason a single SMP member can push F3 through to `main` alone.

> **Design-check note (2026-10-06): the P3-16 row understates what SMP already gates.**
>
> SMP also has run-state gates on its submit and acknowledge paths:
>
> - `submitRunGate` (SMP `proxy.go:1223-1231`, gate 3b at `:1166-1180`);
> - `acknowledgeRunGate` (SMP `execution.go:366-379`, called at `:342`).
>
> Both answer `409 RUN_NOT_ACTIVE` (`spinecore.ErrRunNotActive`, SMP `runstore.go:21-28`). They
> refuse a live execution only when the run is **terminal** (`cancelled`, `failed` or
> `completed`).
>
> This task's `EnforceRunStatus` (§9) is stricter: it refuses every non-`active` status, which
> includes `pending`, `paused`, `committing` and `partially-merged`. That is safe:
>
> - no actor step is live in those states (`committing` is entered only by the merge handler or
>   `CompleteRun`, and `paused` has no production setter);
> - SMP TASK-011 must still state the difference when it opts in.
>
> SMP's member assign and claim paths (`proxy.go:1002-1049`, `execution.go:213-238`) have no run
> gate. The P3-16 SMP cell is correct about those.

### Why the policy is opt-in

The orchestrator's plan requires that a Spine bump does not change SMP until SMP TASK-011 opts in. The code shows that default-on enforcement would break SMP today:

- **SMP human handles are not Spine actors.** Members are bound as `human-<uuid>` handles from `platform.workspace_roles`, and those are "NOT minted into the workspace runtime auth.actors" (SMP `internal/customer/spineembedded/embedded_ready.go:60-70`). SMP's `requireActorType` treats them as type `""` (SMP `executor_gates.go:108-125`). An "actor exists" check against Spine's actor table would refuse every SMP human.
- **SMP humans hold no skills.** The only skill grants are server-side grants to managed agents (SMP `internal/agentregistry/handler.go:38`, `internal/customer/spinecore/skillsstore.go:175`). SMP's `task-default` execute step requires `[implementation]` and review requires `[review]`, so "skills on assign" would strand every human step.
- **SMP's workflows carry preconditions that can never hold mid-run** (Design §8.4):
  - `task-default` review uses `field_value status: In Progress` (SMP `workflows/task-default.yaml:125-129`);
  - `epic-lifecycle` execute uses `artifact_status: In Progress` (SMP `workflows/epic-lifecycle.yaml:45-48`).
  - Today both fail at activation, and the step is still assigned through the unchecked path. Enforcing them at assign would strand every task review and every epic run.

## Design

### 1. API (new file `core/engine/step_entry.go`)

```go
// StepEntryPolicy configures what the engine checks before an actor is bound
// to a step execution, and what happens to live executions when a run ends.
// The zero value is the pre-INIT-023 behaviour: no field is read on any path.
type StepEntryPolicy struct {
    EnforceRunStatus       bool // P3-16: entry and advance points require run.Status == active
    EnforceEligibility     bool // P3-3: actor exists, is active, and matches eligible_actor_ids / eligible_actor_types
    EnforceExecutionMode   bool // P3-15: mode table, Design §5
    EnforcePreconditions   bool // P3-4: engine evaluator at assign/claim; strict evaluation (Design §8)
    RequireSkillsOnAssign  bool // required_skills on AssignStep (ClaimStep already checks them)
    SeparationOfDuties     bool // review-type steps refuse run authors (Design §7)
    TerminateStepsOnCancel bool // P3-16: CancelRun/FailRun/merge-fail close live executions (Design §10)

    // Actors resolves the actor being bound, for the eligibility and mode
    // checks. Nil uses the blocking store's GetActor, which is ClaimStep's
    // lookup today (claim.go:201-213). An embedder whose actors are not all
    // rows in the Spine actor table (SMP human handles) supplies its own.
    Actors EntryActorResolver
}

type EntryActorResolver interface {
    // A *domain.SpineError with ErrNotFound means "no such actor".
    ResolveEntryActor(ctx context.Context, actorID string) (*domain.Actor, error)
}

func StrictStepEntryPolicy() StepEntryPolicy        // every bool true, Actors nil
func (p StepEntryPolicy) IsZero() bool              // no bool set (Actors ignored)
func (o *Orchestrator) WithStepEntryPolicy(p StepEntryPolicy)
func (o *Orchestrator) StepEntryPolicy() StepEntryPolicy // read-only accessor, for wiring tests
```

- **Storage.** Add a field `entryPolicy StepEntryPolicy` to `Orchestrator` (`core/engine/orchestrator.go:12-39`). Add `WithStepEntryPolicy` next to the other `With*` setters (`:100-198`).
- **Shared helpers in the same file:**
  - `requireActiveRun(run, action string) error`, the `EnforceRunStatus` guard;
  - `entryActor(ctx, id)`, which uses the policy resolver when one is set and otherwise `o.loadActor`;
  - `checkStepEntryActor(...)`, which runs the run-status, mode, eligibility, skills and SoD checks;
  - `checkStepEntryPreconditions(...)`;
  - `runAuthor(...)`;
  - `terminateLiveSteps(...)`;
  - the optional interface `activeAssignmentCanceller` (Design §10).
- **Zero-value guarantee.** Every new branch is guarded by a policy bool. `entryActor` with a nil resolver is `loadActor`. A zero-value orchestrator therefore executes byte-for-byte today's logic.

> **Design-check note (2026-10-06): API additions and contract tightening.**
>
> - **"Zero value" means all bools false and `Actors == nil`.** `entryActor` uses a non-nil
>   resolver even when every bool is false, and `IsZero()` ignores `Actors`. So installing only a
>   resolver changes `ClaimStep`'s actor lookup at `claim.go:95`. This is intended for SMP: a
>   resolved `human-<uuid>` handle can then claim a step that has an execution block but no
>   `required_skills`, where today `loadActor` refuses it. Document it on `IsZero`, and do not
>   describe a resolver-only policy as "the zero value".
> - **Resolver contract** (enforced by `entryActor`, not left to the embedder):
>   - The returned `*domain.Actor` must have `ActorID == actorID` exactly. Otherwise the engine
>     answers `ErrNotFound`.
>     - Reason: no alias such as a case variant may resolve to a live actor. SoD (§7) and SMP's
>       submit-ownership check are both exact string compares, so an alias would bind an
>       identity that neither check recognises.
>   - A `*domain.SpineError` returned by the resolver passes through with its code. SMP
>     TASK-011's resolver answers a directory outage with `ErrUnavailable` → 503.
>   - Only untyped errors are wrapped as `ErrInternal`. §2 "Lookup errors" is amended
>     accordingly.
> - **No panic on partial wiring.** When `gatesBinding()` is true and `o.wfLoader` is nil (only
>   possible for hand-built `&Orchestrator{}` test values), `AssignStep` returns `ErrUnavailable`
>   "step-entry policy needs a workflow loader". It must not nil-dereference.
> - **New exported method** `func (o *Orchestrator) TerminateLiveSteps(ctx context.Context,
>   runID, message string)`:
>   - it wraps `terminateLiveSteps`;
>   - it is a no-op unless `TerminateStepsOnCancel` is set;
>   - it exists for the scheduler's run-timeout cancel, which writes the run row without the
>     engine (§10 note).
> - **New optional interface** `assignmentHistory { ListAssignmentsByActor(ctx, actorID string,
>   status *domain.AssignmentStatus) ([]domain.Assignment, error) }`:
>   - the engine type-asserts it on `o.assignments` for the SoD author ledger (§7 note);
>   - `*store.PostgresStore` already has it (`adapters/store/postgres_assignments.go:46`), so
>     there is no interface or store change, and SMP gets it for free (SMP
>     `spinecore/assignmentstore.go:42-47`).
- **Tests that already depend on this.** Existing tests build `&Orchestrator{store, events}` with no `wfLoader` (for example `core/engine/assign_test.go:23`). They keep working because `AssignStep` loads the run and workflow only when `entryPolicy.gatesBinding()` is true. `gatesBinding()` is true if any of the six entry bools is set.

### 2. Check order and error codes

`checkStepEntryActor` runs in this fixed order. The first failure wins, and nothing is written before the bind.

| # | Check | Policy field | Refusal (`domain.ErrorCode` → HTTP) |
|---|---|---|---|
| 1 | Run status is `active` | `EnforceRunStatus` | `ErrConflict` → 409, "run R is cancelled; a step can only be assigned on an active run" |
| 2 | Engine-owned step (`mode: spine_only` or `type: internal`) | `EnforceExecutionMode` | `ErrConflict` → 409 |
| 3 | Mode-implied actor type (§5) | `EnforceExecutionMode` | `ErrConflict` → 409. An unknown actor is `ErrNotFound` → 404 |
| 4 | Actor exists and is `active` | `EnforceEligibility` | `ErrNotFound` → 404, or `ErrForbidden` → 403 for a suspended or deactivated actor |
| 5 | Allow-list and `eligible_actor_types`. Assign only: claim keeps its existing checks at `claim.go:79-113`, unchanged. | `EnforceEligibility` | allow-list → `ErrForbidden` 403; type → `ErrConflict` 409, matching `claim.go:88-90` and `:110-111` |
| 6 | `required_skills`. Assign only: claim keeps `claim.go:116-142`. | `RequireSkillsOnAssign` | `ErrConflict` → 409 "actor missing required skills: [...]". `ErrUnavailable` → 503 if the blocking store cannot list skills (fail closed, unlike claim's silent skip) |
| 7 | Separation of duties (§7) | `SeparationOfDuties` | `ErrForbidden` → 403, naming the step the actor executed |
| 8 | Preconditions (§8). Runs last because it may read git. | `EnforcePreconditions` | `ErrPrecondition` → 412, with the `ValidationResult` detail when `cross_artifact_valid` produced one |

- **Lookup errors.** When actor lookup is unwired (no resolver, and a blocking store that does not support it) the result is `ErrUnavailable` (503). Any other lookup error is wrapped as an internal error.

  > **Design-check note (2026-10-06).** Read "any other lookup error" as "any other **untyped**
  > error". A `*domain.SpineError` from the resolver or the store keeps its code, so
  > `ErrUnavailable` stays 503 and is not collapsed to 500. See the §1 note.
- **HTTP mapping.** Codes map to HTTP through the existing `service/internal/gateway/response.go:80-91` and SMP's `wrapEmbeddedError`.

### 3. `AssignStep` (`core/engine/assign.go:32-75`)

After `findStepExecution` (`:40-43`), add a block that runs only when `o.entryPolicy.gatesBinding()` is true:

1. Load the run (`o.store.GetRun`) and the pinned workflow (`o.wfLoader.LoadWorkflow(run.WorkflowPath, run.WorkflowVersion)`), then call `findStepDef`. A missing step definition is `ErrNotFound`, so the bind fails closed.
2. Compute the effective allow-list: `req.EligibleActorIDs` when non-empty, otherwise `exec.EligibleActorIDs`. This keeps the operator-set restriction semantics at `:67-69`: the actor must satisfy the list the call leaves in place.
3. Call `checkStepEntryActor(ctx, entryAssign, run, wfDef, stepDef, exec, req.ActorID, allow)`, then `checkStepEntryPreconditions(ctx, run, stepDef)`.

Everything else stays as it is: the open-slot recovery (`:55`), the transition (`:56-64`) and the write (`:66-72`). `LookupStepDef` (`:82-92`) is unchanged. Update the doc comment at `:28-31`: under the policy, preconditions and eligibility are the engine's responsibility.

> **Design-check note (2026-10-06): under this design the allow-list does not bind the assigner.**
>
> - **Who writes the list.** `exec.EligibleActorIDs` is written only by `AssignStep` itself
>   (`assign.go:67-69`, plus the gateway fallback `step_assign_fallback.go:90-92`). `RetryStep`
>   copies it (`retry.go:88`). Nothing else sets it.
> - **The consequence.** Step 2 makes `req.EligibleActorIDs` the effective list. So the caller can
>   name `eligible_actor_ids: [self]` (standalone `handlers_workflow.go:40`, `:501`) and pass
>   check 5 for itself.
> - **Why it is not a bypass of a stronger control.** The only writer of the list is the same
>   `step.assign` authority.
> - **SMP impact: none.** SMP never forwards the field (`proxy.go:1037-1045`).
> - **What the implementer must do.**
>   - Keep the designed semantics and say so in the doc comment.
>   - Pin it with the AC 2 row added by this check.
> - **Alternative.** "Narrow-only" (a non-empty stored list may only be replaced by a subset) is
>   Owner question 5. The design's default stays because narrow-only would leave no API to
>   re-target a step to an actor outside a stale list.
> - **Separation of duties is not affected.** Check 7 applies whatever list is passed.

### 4. `ClaimStep` (`core/engine/claim.go:31-198`)

1. After `stepDef` is found (`:71-75`), when `gatesBinding()` is true, call `checkStepEntryActor(ctx, entryClaim, ...)`. For claims it runs checks 1-4 and 7. The existing allow-list (`:79-91`), type (`:100-113`) and skill (`:116-142`) checks stay exactly where they are.
2. Replace `o.loadActor(ctx, req.ActorID)` at `:95` with `o.entryActor(ctx, req.ActorID)`. With a nil resolver this is the same call, so the zero value is unchanged.
3. Call `checkStepEntryPreconditions` immediately before `now := time.Now()` (`:145`), so preconditions are the last check before the assignment row is created at `:158-163`.

### 5. Mode table (applied when `EnforceExecutionMode` is on)

| `execution.mode` (or step type) | Assign | Claim |
|---|---|---|
| `spine_only`, or `type: internal` with any mode | refused, `ErrConflict`. The engine runs these steps via `activateInternalStep` (`core/engine/handlers.go`), which never calls Assign or Claim, so publish progression is unaffected. Recovery for a stuck engine step is `ActivateStep`. | refused |
| `automated_only` | `automated_system` actors only | `automated_system` only. SMP's `assignRunnerStep` (SMP `executor_gates.go:181-246`, binding at `:238`) and its recovery claim (SMP `automation_recovery.go:402`) bind runners, so they stay allowed. |
| `ai_only` | `ai_agent` only. SMP is stricter: it refuses every mid-run `ai_only` bind with `NO_DELIVERY_PATH` (SMP `proxy.go:1027-1028`), and that stays SMP's call. | `ai_agent` only. The SMP agent engine's claim of an `ai_only` entry step stays allowed. |
| `human_only` | `human` only | `human` only |
| `hybrid` or unset | no mode-implied type. `eligible_actor_types`, when declared, applies through check 5 (assign) or `claim.go:100-113` (claim); otherwise any active actor. | same |

The mode-implied types match `resolveAutoActorID`'s inference (`step.go:947-956`).

### 6. Eligibility and actor resolution

- **Assign.** Under `EnforceEligibility`, `AssignStep` requires that:
  1. the actor resolves (`entryActor`),
  2. `Status == active` (`domain.ActorStatusActive`, `core/domain/actor.go:53`),
  3. the actor is in the effective allow-list when that list is non-empty, and
  4. its type is in `eligible_actor_types` when those are declared.
- **Claim.** `ClaimStep` gains only the existence and active-status requirement. That closes the gap SMP works around at SMP `executor_gates.go:205-210` ("ClaimStep checks type and skills but not lifecycle").
- **The resolver hook.** `Actors` lets an embedder answer for actors that are not rows in Spine's actor table. SMP TASK-011 supplies one that maps `human-<uuid>` handles to `{Type: human, Status: active}` while the member's workspace role exists (§15). Spine itself never needs it: standalone actors are all rows in `auth.actors`.

### 7. Separation of duties

- **Rule.** When `SeparationOfDuties` is on and the target step's `Type == review` (`domain.StepTypeReview`, `core/domain/workflow.go:19`), the bind is refused if the candidate is a run author.
- **Who counts as a run author.** The candidate is an author when it is the `ActorID` of any other execution of the same run whose step type is `manual` or `automated`, in any status:
  - a failed, in-progress or released-and-rebound attempt still counts;
  - an execution whose step is missing from the pinned workflow counts too (fail closed).
  - The data comes from `o.store.ListStepExecutionsByRun` and the pinned `wfDef`; no new store method is needed.
- **What does not count:**
  - executions of `review`, `convergence` and `internal` steps, so one person may both `verify` and `review` in SMP's `task-default`, which has two review-type steps (owner question 1);
  - the run initiator, who is not recorded on `runtime.runs` (no schema change; Out of scope).
- **Both binding paths.** The rule applies to `AssignStep` and `ClaimStep`.
- **Auto-selection.** It also applies when `ActivateStep` auto-selects an actor for an `automated_only` or `ai_only` review step (`step.go:73`, `:169-181`). If the selected actor is an author, the step stays `waiting` with an info log, the same as "no match". It is never bound, and a non-author can claim it. `actor.SelectionRequest` (`core/actor/selection.go:23-29`) has no exclusion list, and adding one is not needed here.
- **Rework loops.** The rule is per run. A reviewer who sent work back may execute the rework, but cannot then review that rework.

> **Design-check note (2026-10-06): bypass found. A released executor is not an author under
> the rule as written.**
>
> **The defect.** The "released-and-rebound attempt still counts" bullet is false at `61994fe`.
> Rebinding reuses the same execution row:
>
> - `ReleaseStep` writes `exec.Status = waiting`, `exec.ActorID = ""` on that row
>   (`release.go:72-77`). The next claimant overwrites the `ActorID`.
> - The same happens after the scheduler's restart recovery resets `assigned → waiting`
>   (`adapters/scheduler/recovery.go:186-199`) and a new actor binds.
> - So a rule that reads only `step_executions.ActorID` forgets the first executor.
>
> **The exploit.** It is standalone today, and SMP for claim-bound agents and runners:
>
> 1. Carol claims `execute` and pushes commits to the run branch while bound. TASK-016 admits
>    the bound actor's push.
> 2. Carol releases (`execution.release` is `RoleContributor`, `permissions.go:48`).
> 3. Dave claims and completes `execute`.
> 4. Carol claims `review` and passes SoD, so her own commits reach `main` reviewed only by her.
>
> Probe `_work/task007/designcheck/zz_t007_designcheck_test.go`
> `TestT007_ReleaseErasesExecutorFromStepExecutions` passes at HEAD. After release and rebind,
> `step_executions` shows `execute=dave`, and Carol survives only as
> `claim-run-1-execute-1-carol` with status `cancelled` in `actor_assignments`.
>
> **Amended rule.** The candidate is a run author when either of these holds:
>
> - **(a)** It is the `ActorID` of a `manual` or `automated` execution of the run (as designed).
> - **(b)** It is the `actor_id` of any `actor_assignments` row, in **any** status, whose
>   `run_id` is the run and whose `execution_id` is such an execution. The row is read through the
>   optional `assignmentHistory` interface (§1 note): `ListAssignmentsByActor(ctx, candidate,
>   nil)` filtered by `RunID`. This is one indexed query (`idx_assignments_actor`,
>   `001_initial_schema.sql:229`).
>
> **Wiring cases.**
>
> - **`o.assignments` is nil.** (a) alone is complete for released bindings, because
>   `ReleaseStep` is unavailable without an assignment store (`release.go:34-36`).
> - **`o.assignments` is non-nil but lacks `ListAssignmentsByActor`.** SoD fails closed with
>   `ErrUnavailable`.
> - **Store error.** Fails closed with `ErrUnavailable`, never "not an author".
>
> **Symmetric guard (cheap, closes the parallel-branch order).** When `SeparationOfDuties` is
> on, binding a `manual` or `automated` step is refused (`ErrForbidden`) if the candidate holds a
> **non-terminal** `review` execution of the same run.
>
> - This matters only with divergence branches, where a review and an author step can be live at
>   once.
> - In a linear workflow it never fires: a reviewer who sent work back holds only a terminal
>   review, so rework stays allowed, as designed above.
>
> **Residual, documented and not closed here.**
>
> - `AssignStep` creates no assignment row (`assign.go:66-74`). If an assign-bound execution is
>   reset by restart recovery and re-bound to someone else, the first actor leaves no record.
> - Closing that needs an append-only binding ledger (a schema change) or an assignment row
>   written by `AssignStep`. That second option collides with the unique active-assignment index
>   when recovery left a claim row active. It is listed under Out of scope.
> - SoD also cannot see git authorship: who pushed which commit. TASK-016 limits pushes to the
>   bound actor; together with (b) that covers every push a bound actor made.

### 8. Preconditions under `EnforcePreconditions`

**8.1 Same evaluator everywhere.** Assign and claim call the engine's `o.evaluatePreconditions(ctx, stepDef, run)` (`step.go:724`), the one `ActivateStep` already uses.

- It reads the run's own artifact at `resolveReadRef(run)` (`step.go:808-813`): `HEAD` for standard runs, the run branch for planning runs.
- A refusal is read-only. It is not persisted as `ErrorDetail`, unlike `preparePreconditionFailure` (`step.go:137-157`), because two concurrent binders must not race on the row.
- `validation.EvaluatePreconditions` is not changed (plan constraint). Its callers keep their semantics: SMP `proxy.go:1032`, `executor_gates.go:234` and `automation_recovery.go:365`, and standalone `service/internal/gateway/handlers_workflow.go:488`.

**8.2 Strict field resolution.** `checkFieldPresent` and `checkFieldValue` (`step.go:840-855`) read keys through a new `o.preconditionField(art, key)`.

- When `EnforcePreconditions` is on, `status`, `id`, `type` and `title` read the typed fields (`art.Status`, `art.ID`, `art.Type`, `art.Title`). Every other key reads `art.Metadata[key]` as today.
- This makes `field_value status` mean what `workflow-definition-format.md` §5.2 says.
- It is gated, not unconditional, so that a zero-value embedder's `ActivateStep` returns exactly what it returns today (Decision 6).
- **Effect on Spine's reference workflow.** Without this change, Spine's own `workflows/task-default.yaml` execute step (`field_value status: Pending`, `:48-52`) would be refused for every actor in strict mode. The prototype confirmed that.

**8.3 Strict fail-closed.** Under `EnforcePreconditions` the evaluator stops skipping in three places (owner question 4):

- an unknown precondition type fails (`step.go:783-785`), and this includes the documented-but-unimplemented `custom` type;
- `cross_artifact_valid` with no validator wired fails (`:759-762`);
- `discussions_resolved` with no checker wired fails (`:875-878`).

The zero value keeps the forward-compatible skips.

**8.4 Preconditions that can never hold mid-run.**

- Commit-status metadata (`outcome.commit.status`) is stored by `SetCommitMeta` (`step.go:396-401`; `adapters/store/postgres_runs.go:153-160`). It is written to the artifact only inside `MergeRunBranch` → `preflightCheck` → `applyCommitStatus` (`merge.go:100-107`), which runs at publish time.
- During a run, the run's own artifact therefore keeps its run-start status on both `main` and the run branch.
- A precondition on the run's own artifact that requires a status set by an earlier outcome's `commit` block can never pass. The audit probe flags exactly three:
  - Spine `workflows/epic-lifecycle.yaml:47-50`: execute requires `artifact_status: In Progress`, which `plan.planned` commits;
  - SMP `workflows/epic-lifecycle.yaml:45-48`: the same;
  - SMP `workflows/task-default.yaml:125-129`: review requires `field_value status: In Progress`, which `execute.completed` commits.
- This task:
  1. **Adds a lint.** It adds `workflow.DeferredStatusPreconditions(wf *domain.WorkflowDefinition) []DeferredStatusPrecondition` in a new `core/workflow/precondition_lint.go`.
     - It reports every `artifact_status`, or `field_value` with `field: status`, that has no `config.path` (meaning the run's own artifact).
     - A precondition is reported when its required status is set by some outcome's `commit.status` in the same workflow and is not required by the entry step's own status preconditions.
     - It is an exported helper and a test, not a `workflow.Validate` rule. That way SMP's workflow-write validation output does not change (Decision 11).
  2. **Fixes Spine's reference workflow.** It removes the unsatisfiable precondition from Spine `workflows/epic-lifecycle.yaml` execute (`:47-50`) and bumps `version` to `"1.1"`. Owner question 3 covers the alternative model.
  3. **Tests every reference workflow.** It adds a test in `workflows/reference_workflows_test.go` asserting that the lint reports nothing for every file in `workflows/`.
- SMP's two workflows are fixed by SMP TASK-011 before it opts in (§15).

> **Design-check note (2026-10-06): the lint output is a consumed API, so pin its shape.**
>
> SMP TASK-011 (§3.4, `batch7/E4-TASK-011-engine-policy-adoption.md`) builds a load-time legacy
> loader. The loader removes exactly the preconditions this lint reports, for workspaces that
> keep the old files. So the report must identify a precondition without ambiguity:
>
> ```go
> type DeferredStatusPrecondition struct {
>     StepID string   // step carrying the precondition
>     Index  int      // position in StepDefinition.Preconditions
>     Type   string   // "artifact_status" or "field_value"
>     Status string   // the required status value
>     SetBy  []string // "<step_id>.<outcome_id>" whose commit.status sets it
> }
> ```
>
> - The order is deterministic: by step order in the workflow, then by `Index`.
> - AC 15 asserts the full struct, not only the step and status text.
> - `field_present` on `status` is not reported, because it requires presence, not a value.
>
> **The rule is a heuristic, and the precise statement is narrower.** For any run, the run's own
> artifact keeps its run-start status until merge. A non-entry step's own-artifact status
> precondition can therefore only hold if it equals that start status.
>
> - The designed rule ("set by some outcome's `commit.status`") catches every known case.
> - It would miss a precondition requiring a status that is neither committed nor the start
>   status. Such a precondition is already unsatisfiable from run start, so it fails the same
>   way at activation.
> - Not widening the rule is acceptable. Record the limitation in the helper's doc comment.

### 9. Run-status guard (`EnforceRunStatus`)

`requireActiveRun(run, action)` returns `ErrConflict` unless `run.Status == active`. It is called at:

| Entry point | Where | Why |
|---|---|---|
| `AssignStep`, `ClaimStep` | check 1 in §2 | binding |
| `AcknowledgeStep` | `acknowledge.go`, before the transition at `:61`. Loads the run only when the flag is on. | starting work |
| `IngestResult` | `result.go`, right after `GetRun` (`:79-82`), before the required-outputs failure path at `:107-117`, so a cancelled run's step is not failed and retried | actor submit |
| `SubmitStepResult` | `step.go`, right after `GetRun` (`:301-304`) | engine funnel. It also covers the scheduler step-recovery callback (`service/cmd/spine/serve_builders.go:300-310`). |
| `ActivateStep` | `step.go`, right after `GetRun` (`:38-41`) | stops `DeliverAssignment` (runner dispatch) for a non-active run. Internal callers always reach it on an active run: `startRunCommon` sets `active` first (`run.go:92-102`), and the publish `merge_failed` route-back sets `active` before routing (`handlers.go:207-230`). |
| `RetryStep` | `retry.go`, after `GetRun` (`:23-26`) | no retry execution is created for a non-active run |

`ReleaseStep` and `FailStep` are not guarded: they only move an execution towards `waiting` or `failed`, and both are harmless on a dead run. The publish step is advanced by `advancePublishStepIfAny` (`handlers.go:163-241`), not by `IngestResult` or `SubmitStepResult`, so `committing` runs keep merging. `paused` has no production setter: `PauseRun` (`run.go:519`) has no non-test callers in Spine or SMP.

> **Design-check note (2026-10-06): two more places the run-status rule must reach, and one
> clarification.**
>
> - **`ListStepExecutions` (the pull-model candidate listing, `step_executions.go:44-140`).**
>   - It never looks at `run.Status`. It already loads each run at `:105-114`.
>   - So under `EnforceRunStatus`, pollers keep being offered `waiting` executions of
>     cancelled, failed or timed-out runs, and every claim of them answers 409. That hits a
>     standalone runner on every poll cycle.
>   - **Added:** when `EnforceRunStatus` is set, skip an execution whose run is not `active`.
>     The zero value is unchanged.
>   - Probe `TestT007_CandidateListingOffersCancelledRunSteps` shows HEAD listing a cancelled
>     run's waiting step.
> - **`IngestResult` on a run-terminated execution.**
>   - §10 moves live executions to `skipped`. `IngestResult`'s idempotent terminal branch
>     (`result.go:37-54`) then answers `nil` error with `status: skipped`, which is HTTP 200
>     (probe `TestT007_LateSubmitOnTerminatedExecutionIsSuccess`).
>   - A late runner or agent submit would therefore "succeed" where SMP answers
>     `409 RUN_NOT_ACTIVE` today. SMP TASK-011 found this too (its Purpose item 4): SMP's agent
>     engine would close the cancelled run's record as `completed`.
>   - **Added:** when `EnforceRunStatus` is set and the execution is `skipped` with
>     `ErrorDetail.Classification == run_terminated`, `IngestResult` returns `ErrConflict`
>     "run R is <status>; the step was terminated with the run". It does not take the idempotent
>     branch.
>   - A duplicate submit of a `completed` execution keeps its idempotent 200.
>   - `AcknowledgeStep` already answers 409 from the state machine.
>   - **Coordination:** TASK-008's design check adds a terminal guard to the same branch
>     (TASK-008 §3a). TASK-008 rebases onto this task, so this rule must survive that rebase.
> - **Clarification on "`ActivateStep` internal callers always reach it on an active run".**
>   That is true at the moment of each call. But `SubmitStepResult`'s guard and
>   `advanceToNextStep`'s `ActivateStep` (`step.go:446`) are separated by writes, so a cancel can
>   land in between. `ActivateStep`'s guard then refuses, `advanceToNextStep` logs and returns
>   nil (`step.go:446-448`), and a `waiting` execution is left on the cancelled run. That is the
>   same residual window TASK-008 records under its Out of scope. It is inert behind this
>   section's guards and hidden from pollers by the listing rule above.

### 10. Terminating live executions when a run ends (`TerminateStepsOnCancel`)

- **State machine.** Add `StepTriggerRunTerminated StepTrigger = "run.terminated"` (`core/workflow/step_machine.go:12-22`). It is valid from `waiting`, `assigned`, `in_progress` and `blocked` to `skipped`.
  - It is handled at the top of `EvaluateStepTransition` (`:41`), after the terminal-state check at `:44-54`, so terminal states still refuse it.
  - `skipped` is used because `step_status_check` has no `cancelled` value: `migrations/001_initial_schema.sql:126`, mirrored at SMP `internal/customer/workspace/spinemigrations/001_initial_schema.sql:126`.
- **Classification.** Add `FailureRunTerminated FailureClassification = "run_terminated"` (`core/domain/step.go:41-49`). `IsRetryable` returns false through its default branch.
  - `error_detail` is JSONB with no constraint on `classification` (`001_initial_schema.sql:123`).
  - The plan text called this "run_cancelled". One classification covers cancel and the two failure paths; the message carries the cause (Decision 4).
- **`terminateLiveSteps(ctx, runID, message)`.** When the flag is on:
  1. List the run's executions.
  2. For each non-terminal one, apply the trigger, set `CompletedAt`, and set `ErrorDetail{Classification: run_terminated, Message, StepID, ActorID}`. Write it with `UpdateStepExecution`.
  3. If `o.assignments` implements `activeAssignmentCanceller`, call `CancelActiveAssignmentsForExecution(ctx, execID, now)`. `cancelled` exists in `assignment_status_check` (`001_initial_schema.sql:222`).
  4. Errors are logged and the loop continues: the run is already terminal, and the §9 guards are the backstop.
- **Where it is called.** Only after the run-status write succeeds:
  - in `CancelRun` after `run.go:499-501`, with message `run cancelled`;
  - in `FailRun` after `:465-467`, with `run failed: <reason>`;
  - in `failRunOnMergeError` after `merge.go:456-458`, with `run failed: merge failed permanently`.
  - These are the only three engine paths into `failed` or `cancelled` (`TriggerCancel`, `TriggerStepFailedPermanently`, `TriggerGitCommitFailedPerm`).
- **Events.** No per-step event is emitted, so no `event-schemas.md` change is needed. `run_cancelled` and `run_failed` already signal the end.
- **Store.** Add `(*PostgresStore).CancelActiveAssignmentsForExecution(ctx, executionID string, at time.Time) (int, error)` in `adapters/store/postgres_assignments.go`:

  ```sql
  UPDATE runtime.actor_assignments SET status = 'cancelled', responded_at = $2
  WHERE execution_id = $1 AND status = 'active'
  ```

  - It is not added to the `store.Store` or `engine.AssignmentStore` interfaces. That keeps the stub stores compiling (`service/cmd/spine/stubstore_test.go`, `service/internal/gateway/stubstore_test.go`), along with SMP fakes.
  - The engine type-asserts the optional interface.
  - SMP's `AssignmentStore` is Spine's `*store.PostgresStore` (SMP `internal/customer/spinecore/assignmentstore.go:42-47`), so SMP gets the method with no code change.
- **After termination:**
  - `AcknowledgeStep` on a skipped execution returns 409 from the state machine;
  - `IngestResult` returns its existing idempotent terminal response (`result.go:37-54`) with `status: skipped` and changes nothing;
  - `ReleaseStep` refuses because the execution is terminal (`release.go`).

> **Design-check note (2026-10-06): a fourth run-end path, the late-submit answer, and the
> cancel/bind race.**
>
> - **"These are the only three engine paths" is true of `core/engine`, but not of the
>   process.**
>   - The scheduler's run-timeout cancel writes `cancelled` straight to the store
>     (`adapters/scheduler/run_timeout.go:61`, `UpdateRunStatusAt`). It never calls `CancelRun`.
>   - A timed-out run therefore keeps live executions exactly as P3-16 describes. TASK-008 makes
>     that write a compare-and-swap (its §"Outside the engine") but does not close executions.
>   - **Added:** a scheduler option `WithRunTerminated(fn func(ctx context.Context, runID, message
>     string))` (`adapters/scheduler/options.go`):
>     - it is called only after an applied timeout-cancel write, that is after TASK-008's CAS
>       reports `applied`;
>     - the message is `run cancelled: run timeout`;
>     - the scheduler does not import the engine; it receives a function, like
>       `StepRecoveryFunc`.
>   - `spine serve` wires it in `buildScheduler` (`serve_builders.go:288-319`, the only scheduler
>     `serve` constructs, `serve_wiring.go:383-386`) to `orch.TerminateLiveSteps` (§1 note).
>   - For parity, add `RunTerminatedFn` to `scheduler.ServiceSet` (`multi.go:62-68`, applied at
>     `:175-180`) and to `workspace.ServiceSet` (`service/internal/workspace/pool.go:72-73`), set
>     in `serve_resolver.go` next to `RunFailFn` (`:179`). `NewMultiScheduler` has no production
>     caller at `61994fe`, so this part is dormant, but it keeps the two schedulers aligned.
> - **The gateway's no-orchestrator cancel fallback** (`handlers_workflow.go:241-266`) also
>   writes the run row directly. It runs only when no orchestrator is wired, which means no
>   policy is installed, so nothing changes there.
> - **Late submit.** The "`IngestResult` returns ... `status: skipped`" bullet above is replaced
>   under `EnforceRunStatus` by the `ErrConflict` rule in the §9 note. The idempotent 200 stays
>   only for the zero value.
> - **Not atomic: termination races a concurrent bind.**
>   - `terminateLiveSteps` writes with `UpdateStepExecution`, a blind write (P3-24). A
>     concurrent `AssignStep`, `ClaimStep` or `ActivateStep` that read the row before termination
>     can write it back as `assigned` after it.
>   - The backstop is `EnforceRunStatus`: every later entry point refuses. TASK-008's CAS closes
>     the race itself.
>   - So `TerminateStepsOnCancel` is only safe together with `EnforceRunStatus`.
>     `StrictStepEntryPolicy()` sets both, and `WithStepEntryPolicy` logs a `WARN` when
>     `TerminateStepsOnCancel` is set without `EnforceRunStatus`.
>   - AC 12 asserts the post-state of a quiescent cancel only. It must not claim atomicity.
> - **Stored data.** Runs that were already cancelled or failed before the upgrade keep their
>   live executions; nothing terminates them retroactively.
>   - They are inert: every entry point refuses them under `EnforceRunStatus`, and the listing
>     rule (§9 note) hides them from pollers.
>   - A one-shot sweep is listed under Out of scope. SMP TASK-011 records the same follow-up.

### 11. Standalone wiring (`spine serve`)

- **Policy selection.** A new helper `stepEntryPolicyFromEnv() (engine.StepEntryPolicy, error)` in a new `service/cmd/spine/serve_step_entry.go` reads `SPINE_STEP_ENTRY_POLICY` (owner question 2):
  - `""` or `strict` → `engine.StrictStepEntryPolicy()`;
  - `legacy` → the zero value, with a startup `WARN` naming the four findings;
  - anything else → serve refuses to start. Validate the value once in `serve_cmd.go`, next to the other env parsing (`:134-150`).
- **Both orchestrator builders call `WithStepEntryPolicy`:**
  - `buildOrchestrator` (`serve_builders.go:231-284`), after `WithBranchProtectPolicy` at `:272`;
  - `workspaceOrchestratorBuilder` (`serve_resolver.go:26-184`), after `:90`.
- **Gateway fallback.** The gateway's `storeStepAssigner` fallback (`service/internal/gateway/step_assign_fallback.go:28-97`) binds through the store with no engine. It is used when no orchestrator assigner is wired.
  - Add `ServerConfig.DisableStepAssignFallback bool` (`server.go:276-332`, stored on `Server`). When true, `assignerFor` returns nil instead of the fallback, so `handleStepAssign` answers 503 "step assigner not configured" (`handlers_workflow.go:469-473`).
  - `buildServerConfig` (`serve_wiring.go:202+`, config literal at `:436-460`) sets it to `!policy.IsZero()`.
  - The default `false` keeps every existing gateway test, which relies on the fallback, unchanged.
- **Gateway precondition check.** `handleStepAssign`'s own `validation.EvaluatePreconditions` call (`handlers_workflow.go:481-494`) stays as it is. It is redundant under the policy but harmless.
- **Unchanged.** The scenario harness (`service/scenariotest/harness/runtime.go:146-169`) and `cmd/embed-smoketest` keep the zero value.

> **Design-check note (2026-10-06).** `buildScheduler` (`serve_builders.go:288-319`) gains
> `scheduler.WithRunTerminated(orch.TerminateLiveSteps)` next to `WithRunFail`.
> `workspaceOrchestratorBuilder` sets `ss.RunTerminatedFn` next to `ss.RunFailFn`
> (`serve_resolver.go:179`). Both are the §10 note's run-timeout hook. Under `legacy` the method
> is a no-op, because `TerminateStepsOnCancel` is false.

### 12. Data, format and migrations

- **Database.** No schema change and no migration in `migrations/` or SMP `spinemigrations/`.
- **New values in unconstrained columns.** `skipped` is already allowed for step status, `cancelled` for assignment status, and `run_terminated` sits in JSONB.
- **Workflow files.** Spine `workflows/epic-lifecycle.yaml` loses one precondition and its version goes from `1.0` to `1.1`. No workflow format change.

### 13. Backward compatibility

- **Zero value is today.** No Spine caller changes behaviour unless it calls `WithStepEntryPolicy`. The only always-on code change is that `ClaimStep` calls `entryActor` instead of `loadActor`, which is the same call when `Actors` is nil.
- **Prototype check.** A scratch overlay prototype of this design (not committed) confirmed the claim. At zero value, these passed with no test edits:
  - `go build ./...`;
  - `go test ./core/... ./adapters/... ./service/... ./workflows/` in Spine;
  - SMP `go build ./cmd/... ./internal/...`;
  - SMP `go test ./internal/customer/spineembedded/... ./internal/customer/spinecore/... ./internal/customer/run/...`.

  > **Design-check note (2026-10-06).** No prototype file was kept (there is no
  > `_work/task007/` prototype), so the "prototype confirmed" claims here and in §8.2 and AC 14
  > could not be re-run.
  >
  > - The zero-value claim holds by construction: every new branch is flag-guarded, and
  >   `entryActor` with a nil resolver is `loadActor`.
  > - AC 19 is the binding check.
  > - The design-check additions keep that property. The listing filter, the late-submit
  >   `ErrConflict`, the SoD ledger and the scheduler hook are all flag-guarded or no-ops at the
  >   zero value. The new scheduler option is unset unless `serve` wires it.
- **Standalone behaviour changes**, because `spine serve` defaults to strict:
  - assign and claim refusals (403, 404, 409, 412);
  - cancelled and failed runs' live executions become `skipped`;
  - self-review is refused;
  - Spine's epic-lifecycle execute step can now be assigned (its precondition is removed).
  - Operators with custom workflows can set `SPINE_STEP_ENTRY_POLICY=legacy` during migration.
  - Document all of this in `CHANGELOG.md` under "Changed (standalone)".
- **Exported API.** Additive only: new types, functions, a method, a trigger constant, a classification constant and a store method. No exported signature changes.

### 14. Documentation

- `architecture/engine-state-machine.md`:
  - §2.2: the cancel and fail effects name `run.terminated → skipped`, and the `paused → cancelled` row too;
  - §3.2: add `run.terminated` rows, and amend the `step.assign` and `step.claim` guards to reference the step-entry policy;
  - §3.3.1: add `run_terminated`;
  - §6.2: an actor may claim, but the engine enforces eligibility, mode, SoD and preconditions under the policy.
- `architecture/workflow-definition-format.md` §5.2:
  - `field_value` and `field_present` on `status`, `id`, `type` and `title` read the typed fields under strict evaluation;
  - add the deferred-commit rule from §8.4 and the lint;
  - note that `custom` is unimplemented and fails closed under strict evaluation.
- `architecture/actor-model.md` §7.3: replace "Workflow definitions may declare step-level constraints ... (e.g., a human reviewer should not review their own work)" with the SoD rule and the policy flag.
- `architecture/security-model.md`: a short "Step-entry policy" subsection listing the checks and the standalone default.
- `KNOWN-LIMITATIONS.md`: add an entry. Under `legacy`, and for embedders that have not opted in, P3-3, P3-4, P3-15 and P3-16 remain.

### 15. SMP impact, and what SMP TASK-011 must change

- **On the bump alone.** SMP does not call `WithStepEntryPolicy`, so bumping `.spine-revision` past this task changes nothing in SMP:
  - SMP's three `validation.EvaluatePreconditions` callers keep their behaviour: `proxy.go:1032` before `AssignStep` at `:1041`, `executor_gates.go:234` before `ClaimStep` at `:238`, and `automation_recovery.go:365` before `ClaimStep` at `:402`.
  - So do SMP's other engine callers: `execution.go:225` (claim), `proxy.go:928` (`CancelRun`), `proxy.go:966` and `executor_reconcile.go:340` (`ActivateStep`), and `embedded_ready.go:193` and `proxy.go:1096` (`IngestResult`).
- **To opt in, SMP TASK-011 must do the following, in order:**
  1. **Fix its workflows first.**
     - Remove SMP `workflows/task-default.yaml:125-129` (review `field_value status: In Progress`) and `workflows/epic-lifecycle.yaml:45-48` (execute `artifact_status: In Progress`), and bump their versions.
     - Backfill them into existing workspaces through the INIT-020 workflow-seeding path.
     - Add a test that runs `workflow.DeferredStatusPreconditions` over SMP `workflows/*.yaml` and expects nothing.
     - Without this step, `EnforcePreconditions` strands every task review and every epic run.
  2. **Supply `Actors`.** It resolves through Spine's `PostgresStore.GetActor` (runtime `auth.actors`: agents, runners, bootstrap admin) first. For `human-<uuid>` handles it then consults `platform.workspace_roles`:
     - a handle with a live role row resolves to `{Type: human, Status: active}`;
     - a removed member resolves to `ErrNotFound`.
     - Without the resolver, `EnforceEligibility` and `EnforceExecutionMode` refuse every SMP human.
  3. **Wire the policy** in `spinecore.newOrchestratorWithRunStore` (SMP `internal/customer/spinecore/factory.go:69-253`, after `WithBranchProtectPolicy` at `:209`):
     - `EnforceRunStatus`, `EnforceEligibility`, `EnforceExecutionMode`, `EnforcePreconditions` and `TerminateStepsOnCancel` = true;
     - `RequireSkillsOnAssign` = false, because SMP humans hold no skills;
     - `SeparationOfDuties` = the owner's decision for SMP. A single-member workspace could never complete a review with it on, so TASK-011 must decide and say so.
  4. **Optionally retire duplicate SMP gates.** Under `EnforcePreconditions`, `ClaimStep` evaluates every precondition type, so `uncheckablePrecondition` (`PRECONDITION_NOT_CHECKABLE`, SMP `executor_gates.go:160-168`) is no longer needed. SMP's mode switch and `claimModeGate` stay, because SMP's `ai_only` rule is stricter.
  5. **Absorb the new behaviours:**
     - runners acknowledging a `skipped` execution get 409;
     - `IngestResult` on a skipped execution returns 200 `status: skipped`;
     - the reconciler's `ActivateStep` on a non-active run returns 409 (SMP `executor_reconcile.go:340`);
     - the UI shows `skipped` with `run_terminated`.
  6. **Run the SMP integration suites with `TEST_SPINE_DB`**, plus new SMP tests: member A executes, A is refused on review, B is accepted; a cancelled run's executions are `skipped`.

> **Design-check note (2026-10-06): §15 corrections. SMP TASK-011 now exists as
> `batch7/E4-TASK-011-engine-policy-adoption.md`.**
>
> **1. Step 1's "backfill through INIT-020 seeding" cannot work.**
>
> - The seed backfill is add-if-never-existed (SMP `workspace/workflow_backfill.go:22-26`,
>   `SeedBackfillRule`). Existing workspaces keep `task-default` v8.0 and `epic-lifecycle` v1.0.
> - In-flight runs stay pinned to the projected source commit. SMP's loader refuses any other ref
>   with `ErrWorkflowVersionMismatch` (`spinecore/workflow_loader.go:115-118`).
> - SMP TASK-011 therefore keeps the new files for new workspaces and adds a load-time
>   legacy loader that drops exactly the preconditions `DeferredStatusPreconditions` reports
>   (its §3.4).
> - That makes the lint's output shape a consumed API, now pinned in the §8.4 note and AC 15.
>   Step 1 should read: "fix the corpus for new workspaces; neutralise the two deferred
>   preconditions at load time for existing ones".
>
> **2. The SMP caller inventory (bullet 1) is incomplete.** These engine entry points also
> change behaviour once SMP opts in, and TASK-011 must account for each:
>
> - `automation_recovery.go:409` and `:419`: `ActivateStep`, recovery re-activation. These meet
>   `EnforceRunStatus` and the SoD auto-selection rule.
> - `execution.go:345`: `AcknowledgeStep`, behind SMP's `acknowledgeRunGate`.
> - `artifact/handler.go:289`: `CreateEntry` auto-assigns the drafting step to its creator via
>   `Runs().AssignStep`. This meets `EnforceEligibility`, `EnforcePreconditions` (the draft step's
>   `artifact_status: Draft` reads the planning branch, `resolveReadRef`) and the resolver.
> - `internal/engine/runner.go:280`: the agent engine's entry-step `ClaimStep`, through
>   `adapters.go`. This meets the mode table (`ai_agent`) and preconditions.
> - `internal/engine/adapters.go:149`: `ActivateStep`, used by the agent engine.
>
> **3. The run gates differ.** See the note after the "Who can exploit" table: SMP refuses
> terminal runs, and this task refuses every non-`active` run.
>
> **4. Late submit.** The §9 note's `ErrConflict` on run-terminated executions keeps SMP's F12
> `409` contract for runners and the agent engine. SMP TASK-011 §4.4 keeps its own gate as
> well.
>
> **5. SoD in SMP.**
>
> - SMP TASK-011 defaults `SeparationOfDuties` to **off** (its Owner question 2), because
>   `CreateEntry` binds the creator to `draft` and the creation workflows' reviews are
>   `human_only`.
> - With SoD off, the audit's F3 amplifier (one member executes and approves) stays open in the
>   pilot. F3 then rests on TASK-006 and SMP TASK-010 alone.
> - That is an owner decision: see Owner question 7.

### 16. Coordination with sibling tasks

- **TASK-008** (submit ownership, CAS; depends on this task) also edits `IngestResult`. This task adds only the `requireActiveRun` call after `GetRun`. TASK-008 may add submit-side fields to `StepEntryPolicy` or define its own policy, and should reuse `requireActiveRun`.
- **TASK-013** (admin-only skills, F6) makes `required_skills` meaningful in standalone. Until it lands, a contributor can self-grant `review`, but SoD still refuses self-review.
- **TASK-006** (governance-path guard on merges, F3) is the primary F3 fix. SoD here removes the single-actor chain the audit calls the amplifier.
- **TASK-017** (declared status lifecycle) may change when commit status is applied. If it ever applies status mid-run, `DeferredStatusPreconditions` must be revisited and the reference test will tell.
- **SMP TASK-011** consumes this. SMP TASK-012 bumps `.spine-revision`.

> **Design-check note (2026-10-06): more sibling interplay.**
>
> - **TASK-008.**
>   - It also makes the scheduler's run-timeout write a CAS. The §10 `WithRunTerminated` hook
>     fires only on its `applied` result.
>   - Its §3a terminal guard sits in the same `IngestResult` branch as the §9 note's
>     run-terminated `ErrConflict`. Agree the order before either is built: run-terminated
>     `ErrConflict` first, then TASK-008's guard.
>   - `terminateLiveSteps` writes go through TASK-008's `writeStepExecution` helper, as its AC 13
>     already expects.
> - **TASK-016** (run-branch push admission) admits a non-operator only while they are bound to
>   the live execution. The SoD ledger (§7 note) then covers every non-operator who could have
>   pushed: anyone ever bound to an author execution.
>   - Together they bound "who wrote it" against "who reviews it". Neither does it alone.
>   - **Gap 1:** the documented assign-reset residual (Owner question 6).
>   - **Gap 2:** under TASK-016's Owner question 1 default, operators and admins may push to any
>     run branch unbound. SoD does not see their pushes. That is within operator authority in
>     standalone, and SMP members are not operators.

## Decisions

1. **One policy struct with independent switches, not a single on/off flag.** SMP needs most checks but not skills-on-assign. It also needs its own actor resolver and may want SoD off for solo workspaces. A single flag would force all or nothing. Independent bools also allow staged opt-in.
2. **Enforce in the engine, not in each caller.** The callers already disagree. The standalone gateway checks only `cross_artifact_valid`. SMP has its own mode and type gates (SMP `executor_gates.go`, `proxy.go:1024-1031`). P3-3 is precisely that the engine's `AssignStep` checks nothing, and an engine-level funnel is the only place both deployments share.
3. **Opt-in with the zero value meaning today, not default-on.** The evidence is in Purpose ("Why the policy is opt-in"): default-on breaks SMP humans, skill-less human steps, and SMP's review and epic preconditions. Standalone gets strict by default through `spine serve` wiring instead.
4. **Use `skipped` with a `run_terminated` classification, not a new `cancelled` step status.**
   - A new status needs a migration in Spine and in SMP's schema mirror, and changes `IsTerminal` consumers.
   - `skipped` is already terminal and immutable (§3.4).
   - One classification name for all three paths (rather than the plan's "run_cancelled") keeps consumers simple, and the message carries the cause.
5. **Precondition refusals at entry are read-only.** Persisting `ErrorDetail` on refusal, as activation does, would add a write that races concurrent binders and would overwrite the activation's record.
6. **Strict field resolution is gated by `EnforcePreconditions`.**
   - Fixing `field_value status` unconditionally would change `ActivateStep` for every zero-value embedder. For example, SMP's `POST .../activate` (`proxy.go:957-970`) would answer 200 instead of 412 for a step with a `field_value status` precondition.
   - That breaks the "no SMP change until opt-in" contract.
   - Activation and entry use the same evaluator, so they never disagree under one policy.
7. **SoD is keyed on step type `review`, not on role or skill.** Step type comes from the workflow version the run is pinned to. Skills are mutable and, in standalone, self-grantable (F6). Role is a workspace-wide property and does not say who did this run's work.
8. **An actor-resolver hook, not "mint every SMP human into `auth.actors`".** Minting is an SMP data-model change with a backfill, and it would duplicate membership state. The hook keeps Spine generic.
9. **On auto-selection, a run author leaves the step `waiting` rather than being swapped for another candidate.** `actor.SelectionRequest` has no exclusion list. Leaving the step waiting is fail-closed and lets a non-author claim it.
10. **`RequireSkillsOnAssign` fails closed (503) when no skill store is wired**, unlike `ClaimStep`'s silent skip at `claim.go:116-121`. A switch that asks for enforcement must not silently do nothing. Claim's behaviour is left alone to keep the zero value byte-identical.
11. **The deferred-status lint is an exported helper and a test, not a `workflow.Validate` rule.** Adding it to validation would change validation output on SMP's workflow write paths (SMP's `workflowwriter`) before SMP opts in. SMP TASK-011 can promote it.
12. **The run-status guard covers acknowledge, submit, activate and retry as well as assign and claim.** P3-16 is "engine step entry points never check run status", and this task owns P3-16 entirely. Leaving submit and activate open would leave the runner-dispatch half of the finding.
13. **Rejected: re-validating preconditions at submit time.** That would be needed only if preconditions could flip between bind and submit. Submit-side integrity is TASK-008's scope (P3-17, P3-24).
14. **(Design check.) The SoD author ledger is `step_executions` plus `actor_assignments` in any status, not `step_executions` alone.** Rebinding reuses the execution row, so release and rebind erase the first executor from `step_executions` (§7 note). `actor_assignments` is the only durable record of a claim, and `ListAssignmentsByActor` already exists. Rejected for now: an append-only binding ledger (schema change; it would also cover assign-bound rows reset by restart recovery). It is a follow-up.
15. **(Design check.) Under `EnforceRunStatus`, a submit on a run-terminated execution is `409`, not the idempotent `200`.** The idempotent branch exists so that a duplicate submit of the same completed outcome is harmless. Applied to `skipped` executions, it tells a late runner or agent that its work was accepted. SMP's F12 contract and the agent engine depend on the 409 (§9 note).
16. **(Design check.) Pollers are not offered executions of dead runs.** Under `EnforceRunStatus`, the candidate listing filters on `run.Status == active`. Otherwise every poll cycle produces a stream of 409s and every stored pre-upgrade live execution is offered forever.
17. **(Design check.) The run-timeout cancel gets a hook, not a rewrite into `CancelRun`.** `CancelRun` also deletes the run branch (`run.go:507-513`), which the timeout path deliberately does not do today. The hook adds only the execution cleanup, so timeout semantics otherwise stay unchanged.

## Owner questions

1. **Separation-of-duties scope.** Which earlier executions disqualify a reviewer?
   - **Default:** any execution, in any status, of a `manual` or `automated` step of the same run, bound to the same actor id. Review, convergence and internal executions do not disqualify, so the same person may `verify` and `review` in SMP's `task-default`.
   - **Alternatives:** count only completed executions; also forbid one actor holding two review steps of the same run; also exclude the run initiator. The last needs a `runtime.runs` column, which is a schema change, so it would go to a follow-up.
2. **Standalone default.** Should `spine serve` install `StrictStepEntryPolicy()` (every check, including SoD and skills-on-assign) by default, with `SPINE_STEP_ENTRY_POLICY=legacy` as a logged escape hatch?
   - **Default:** yes, strict, with the escape hatch, to be removed in a later release.
   - **Alternative:** strict with no escape hatch.
3. **Status preconditions mid-run.** Commit status is applied only at publish, so a status precondition on the run's own artifact can only check the run-start status.
   - **Default:** keep "`main` is truth" and remove the unsatisfiable preconditions (Spine epic-lifecycle here; SMP's two in SMP TASK-011), with the lint guarding against new ones.
   - **Alternative:** evaluate own-artifact status preconditions against the pending `CommitMeta["status"]`. Rejected as the default because it breaks rework loops: after `execute.completed` commits `In Progress`, a `validate.failed → execute` rework sees `In Progress` and fails execute's `Pending` precondition.
4. **Fail closed under strict evaluation.**
   - **Default:** yes. With `EnforcePreconditions`, an unknown precondition type (including the documented-but-unimplemented `custom`), `cross_artifact_valid` without a validator, and `discussions_resolved` without a checker all refuse rather than pass.
   - **Alternative:** keep the forward-compatible skips even in strict mode.

> **Design-check additions (2026-10-06).**

5. **Allow-list on assign.** May an assigner replace a stored `eligible_actor_ids` with a list
   that admits itself?
   - **Default:** yes, as designed. `step.assign` is the only writer of the list (§3 note), and
     SMP never forwards the field.
   - **Alternative:** narrow-only. A non-empty stored list may only be replaced by a subset.
     This would leave no API to re-target a step to an actor outside a stale list.
6. **SoD residual for assign-bound rows.** Accept the documented residual, or add a binding
   ledger now?
   - The residual: an `AssignStep` binding leaves no assignment row, so if restart recovery
     resets the row and another actor binds, the first executor is forgotten (§7 note).
   - **Default:** accept the residual. It needs a process restart while a step is `assigned`,
     followed by a rebind. The follow-up is a binding ledger, which is a schema change.
   - **Alternative:** write an `assign-<exec>-<actor>` assignment row from `AssignStep` under
     `SeparationOfDuties`. This must first cancel any stale `active` row for the execution,
     because of the unique index (`001_initial_schema.sql:225-227`).
7. **SoD in the SMP pilot.** SMP TASK-011 defaults `SeparationOfDuties` off for solo workspaces.
   Should the pilot keep the F3 amplifier open?
   - **Spine-side default:** no change here; the switch exists.
   - **Recommendation to the SMP owner:** on by default, with an explicit per-workspace opt-out
     that is logged and shown in the UI.
   - Leaving it off means one member can still execute and approve their own run, and F3 rests
     on the governance-path guard alone.

## Acceptance Criteria

**Red before green.**

- The first commit on the task branch adds `StepEntryPolicy`, `StrictStepEntryPolicy`, `WithStepEntryPolicy` and the `run.terminated` and `run_terminated` constants as inert plumbing: fields are stored but never read. It also adds the tests below.
- The implementer records in Implementation Notes that AC 2-12, 14 and 16 fail on that commit, and pass once the checks land.
- Each AC names the mutation that must turn it red again.
- *Design-check note (2026-10-06):* AC 23-27 join the red-on-inert-plumbing list. The inert
  commit also adds `TerminateLiveSteps` (a no-op) and the `WithRunTerminated` option (stored,
  never called).

**Tests that pin the zero value.**

1. **`core/engine/step_entry_legacy_test.go`** commits the bodies of these audit PoCs, renamed `TestStepEntryLegacy_*`, with no policy installed. They pass and so pin today's permissive behaviour:
   - `poc/engine-authz/poc_assign_bypass_test.go`, `poc_precondition_bypass_test.go` and `poc_spine_only_claim_test.go`;
   - `poc/governance/assign_bypass_test.go`, as an `engine_test` file;
   - `poc/skeptic-selfreview/poc_selfreview_test.go`;
   - `poc/verify-r1-6/assign_skeptic_engine_test.go`, as `engine_test`;
   - `poc/verify-r1-7/poc_r17_precondition_test.go`.

   *Mutation:* making any check run when its flag is false (for example `gatesBinding()` returning true for the zero value) fails at least one of them.

   *Design-check note (2026-10-06):*

   - **Verified at HEAD.** All seven PoCs compile together in one overlay (`core/engine`, both
     `engine` and `engine_test` packages) and pass at `61994fe`
     (`_work/task007/designcheck/overlay.json`).
   - **Keep only what this task pins.** The committed `TestStepEntryLegacy_*` bodies keep only
     the **binding** assertions: the bind succeeds, the bound `ActorID`, and the execution status
     after the bind.
   - **Drop the assertions that belong to sibling tasks:**
     - merge mechanics (`MergeOpts.Source == "spine/run/t-1"`, merge counts), which TASK-005
       binds to a reviewed SHA;
     - post-submit routing through `IngestResult` or `SubmitStepResult`, which TASK-008 changes
       (submitter identity and CAS).
   - Otherwise those tasks would turn this task's legacy pins red for reasons unrelated to step
     entry.

**Regression tests derived from the PoCs (policy on).**

2. **`TestStepEntry_AssignRefusesIneligibleActor`** (from `poc/engine-authz/poc_assign_bypass_test.go`), with `StrictStepEntryPolicy()` on the PoC's workflow (review: `human_only`, `[human]`, `[review]`, allow-list `[alice-reviewer]`):

   | Call | Expected |
   |---|---|
   | `AssignStep` of `mallory` (`ai_agent`) | `ErrConflict` |
   | `no-such-actor` | `ErrNotFound` |
   | a suspended human | `ErrForbidden` |
   | a human with `review` who is not on the allow-list | `ErrForbidden` |
   | a human on the allow-list without `review` | `ErrConflict` "missing required skills" |
   | `alice-reviewer` | bound |

   After every refusal the execution is still `waiting` with no actor, and the recording git operator has recorded zero merges. *Mutation:* deleting any one of checks 3-6 in `checkStepEntryActor` fails the matching row.

   *Design-check rows (2026-10-06):*

   | Call | Expected |
   |---|---|
   | `AssignStep` of a human with `review` not on the stored list `[alice-reviewer]`, passing `EligibleActorIDs: [self]` | bound (Owner question 5 default). The stored list becomes `[self]`. The row pins the documented semantics; flipping to narrow-only must change this row deliberately. |
   | `AssignStep` of a step id absent from the pinned workflow (the PoC's `ghost-step`) | `ErrNotFound`, nothing written |
3. **`TestStepEntry_PreconditionFailedStepNotBindable`** (from `poc/engine-authz/poc_precondition_bypass_test.go`).
   - With the task `Draft` at `HEAD` and `EnforcePreconditions`, both `ClaimStep` and `AssignStep` return `ErrPrecondition`. No assignment row is created and the execution is unchanged.
   - With an open discussion thread, the result is `ErrPrecondition` too.
   - After the task becomes `Pending` and threads are resolved, `ClaimStep` succeeds.
   - *Mutation:* removing the `checkStepEntryPreconditions` call from either `AssignStep` or `ClaimStep` fails it.
4. **`TestStepEntry_R17_SMPFunnelRefusesDraftTask`** (from `poc/verify-r1-7`) replays SMP's assign sequence: `LookupStepDef` → `validation.EvaluatePreconditions` → `AssignStep`.
   - `validation.EvaluatePreconditions` still returns `"passed"`, which pins that helper as unchanged.
   - `AssignStep` with `EnforcePreconditions` returns `ErrPrecondition` for a `Draft` task, so neither execute nor review can be bound.
5. **`TestStepEntry_SpineOnlyStepNotBindable`** (from `poc/engine-authz/poc_spine_only_claim_test.go`).
   - With only `EnforceExecutionMode`, `ClaimStep` and `AssignStep` on the `spine_only`/`internal` publish step return `ErrConflict`.
   - The run stays `active` and zero merges are recorded.
   - *Mutation:* deleting check 2 fails it.
   - *Design-check addition (2026-10-06):* with `StrictStepEntryPolicy()`, `ActivateStep` on the
     same `waiting` publish step still runs the merge handler, and exactly one merge is recorded.
     This pins §5's claim that engine-step recovery is unaffected.
6. **`TestStepEntry_ModeTable`** is table-driven over the rows of §5 × actor type {human, ai_agent, automated_system} × operation {assign, claim}.
   - The expected outcome is exactly §5.
   - The cases include an automated runner claiming `automated_only` (allowed), an `ai_agent` claiming an `ai_only` entry step (allowed), and an unknown actor on `human_only` (`ErrNotFound`).
   - *Mutation:* changing any entry of `modeActorType` fails a row.
7. **`TestStepEntry_SeparationOfDuties`** (from `poc/skeptic-selfreview`), with only `SeparationOfDuties`:

   | Case | Expected |
   |---|---|
   | `member-1` (actor of the completed `execute` execution) assigned or claiming `review` | `ErrForbidden`, message naming `execute` |
   | actor of a `failed` execute attempt | refused |
   | actor who only executed a review-type `verify` step, binding to `review` | allowed |
   | `member-2` | allowed |
   | `member-1` re-binding to a rework `execute` | allowed |
   | `ActivateStep` of an `ai_only` review step whose selector returns the author | step stays `waiting` with no actor |
   | same, when the selector returns a non-author | bound |

   *Mutation:* dropping the `Type == review` condition, or the manual/automated author filter, fails it.

   *Design-check rows (2026-10-06)* (§7 note; probe `TestT007_ReleaseErasesExecutorFromStepExecutions`):

   | Case | Expected |
   |---|---|
   | `carol` claims `execute`, releases; `dave` claims and completes `execute`; `carol` assigned to or claiming `review` | `ErrForbidden`, message naming `execute` (author through `actor_assignments`) |
   | same, with `o.assignments` wired to a store that lacks `ListAssignmentsByActor` | `ErrUnavailable` (fail closed) |
   | same, with `ListAssignmentsByActor` returning an error | `ErrUnavailable`, never bound |
   | divergence fixture: `erin` holds a non-terminal `review` execution, then is assigned or claims a `manual` branch step of the same run | `ErrForbidden` (symmetric guard) |
   | `erin`'s review is `completed` (`needs_rework`), then `erin` binds the rework `execute` | allowed |

   *Mutation:* removing the assignment-history lookup fails the first row. Removing the
   symmetric guard fails the fourth.
8. **`TestStepEntry_GovernanceSelfApprovalRefused`** (`engine_test`; from `poc/governance/assign_bypass_test.go` and `poc/verify-r1-6`) runs `StartRun` → assign execute → submit → assign review under two policies.
   - **(a) `StrictStepEntryPolicy()`:** the executor's review assign is refused.
   - **(b) An SMP-shaped policy:** all checks except `RequireSkillsOnAssign`, plus an `Actors` resolver that answers `human-*` handles as active humans holding no skills.
     - A skill-less `human-*` executor's execute assign succeeds.
     - The executor's review assign is refused (`ErrForbidden`, SoD).
     - A second `human-*` reviewer is bound, and the run reaches `committing` or `completed`.
   - *Mutation:* bypassing the resolver (`entryActor` → `loadActor`) fails (b).
9. **`TestStepEntry_FieldValueReadsTypedFields`** (from `poc/skeptic-precond`) parses real Markdown with `artifact.Parse`.
   - With the zero value, `field_value status: In Progress` fails, as today.
   - With `EnforcePreconditions` it passes when the artifact's status is `In Progress` and fails otherwise. The same holds for `id`, `type` and `title`, and for `field_present status`.
   - The parser contract is unchanged: `core/artifact` still omits `status` from `Metadata`. The PoC's parser assertion is kept as a parser test.
   - *Mutation:* removing the `status` case from `preconditionField` fails it.
10. **`TestStepEntry_StrictPreconditionsFailClosed`.** Under `EnforcePreconditions`, each of these makes `evaluatePreconditions` return false, while under the zero value they return true:
    - precondition type `custom`;
    - type `bogus`;
    - `cross_artifact_valid` with a nil validator;
    - `discussions_resolved` with a nil checker.
11. **`TestStepEntry_RunStatusGuards`.** For runs in `cancelled`, `failed`, `paused` and `committing`, each with a legacy live `waiting`/`assigned`/`in_progress` execution, and `EnforceRunStatus` only:
    - `AssignStep`, `ClaimStep`, `AcknowledgeStep`, `IngestResult`, `SubmitStepResult` and `ActivateStep` return `ErrConflict` naming the run status;
    - `RetryStep` creates no new execution;
    - no execution changes;
    - the actor gateway records zero `DeliverAssignment` calls.

    Under the zero value the same calls succeed as today. *Mutation:* deleting `requireActiveRun` from any one entry point fails that sub-case.

    *Design-check note (2026-10-06):* add `partially-merged` and `pending` to the run statuses.
    The candidate-listing and late-submit rules have their own ACs (24, 25).
12. **`TestStepEntry_TerminateStepsOnRunEnd`.** With `TerminateStepsOnCancel`, each of `CancelRun` (from `active` and from `paused`), `FailRun` and `failRunOnMergeError`:
    - moves every `waiting`, `assigned`, `in_progress` and `blocked` execution of the run to `skipped`, with `ErrorDetail.Classification == run_terminated`, a message naming the cause, and `CompletedAt` set;
    - leaves `completed` and `failed` executions untouched;
    - calls a fake `activeAssignmentCanceller` once per terminated execution.

    Afterwards, `AcknowledgeStep` returns `ErrConflict` and `IngestResult` returns `status: skipped` without a write. Under the zero value, executions are unchanged. *Mutation:* removing the call from any of the three run-end paths fails that sub-case.

    *Design-check note (2026-10-06):*

    - **Superseded expectation.** With `StrictStepEntryPolicy()` (which sets `EnforceRunStatus`),
      `IngestResult` on a terminated execution returns `ErrConflict` with no write (AC 25). The
      "`status: skipped` without a write" expectation holds only for a policy with
      `TerminateStepsOnCancel` alone.
    - **Added sub-case: `CancelRun` from `partially-merged`.**
    - **Added sub-case: the run-timeout path is covered by AC 26.**
    - **The test asserts a quiescent post-state only.** It must not claim atomicity against a
      concurrent bind (§10 note).
    - **A policy with `TerminateStepsOnCancel` and not `EnforceRunStatus`** makes
      `WithStepEntryPolicy` log one `WARN`. Assert it with a captured logger.
13. **State machine.** `core/workflow/step_machine_test.go` adds:
    - `run.terminated` from `waiting`, `assigned`, `in_progress` and `blocked` → `skipped`;
    - from `completed`, `failed` and `skipped` → `ErrConflict`.

    Existing transition tests are unchanged.

**Workflows, standalone and SMP.**

14. **`TestStepEntry_ReferenceWorkflowsUnderStrictPolicy`** (`engine_test`) uses `workflow.Parse` on Spine's real `workflows/task-default.yaml`, `workflows/epic-lifecycle.yaml` and `workflows/adr.yaml`.
    - It drives each to `completed` under `StrictStepEntryPolicy()`.
    - It uses distinct executor and reviewer actors holding exactly the declared skills, with the artifact in its entry status at `HEAD` and a parent link.
    - In each review step, the executor's bind is refused and an independent reviewer's bind succeeds.
    - *Mutation:* reverting the epic-lifecycle edit fails at execute (`ErrPrecondition`), and removing the strict `status` resolution fails task-default at execute. The prototype confirmed task-default end-to-end.
15. **Lint.** `core/workflow/precondition_lint_test.go`:
    - `DeferredStatusPreconditions` reports `execute: artifact_status In Progress` for a fixture equal to Spine `epic-lifecycle.yaml` at `61994fe`;
    - it reports `review: field_value status In Progress` for a fixture with SMP `task-default.yaml` v8.0's execute and review blocks;
    - it reports nothing for Spine `task-default.yaml`.

    `workflows/reference_workflows_test.go` asserts it reports nothing for every `*.yaml` in `workflows/`.

    *Design-check note (2026-10-06):* each case asserts the full `DeferredStatusPrecondition`
    value (§8.4 note):

    - `StepID`, `Index`, `Type`, `Status` and `SetBy`;
    - for example, `{StepID: "review", Index: 0, Type: "field_value", Status: "In Progress",
      SetBy: ["execute.completed"]}` for the SMP fixture;
    - a precondition that is not first in its step's list is reported with its real `Index`.

    *Mutation:* reporting `Index` 0 for every entry fails it.
16. **`TestStepEntry_SMPTaskDefaultContract`** uses an inline workflow fixture with SMP `workflows/task-default.yaml` v8.0's execute and review steps, and the SMP-shaped policy from AC 8.
    - `AssignStep(execute, human-<uuid>)` with the task `Pending` at `HEAD` succeeds.
    - `AssignStep(review, <other human>)` returns `ErrPrecondition`.
    - The test comment names SMP TASK-011 step 1 as the prerequisite. It pins why SMP must fix its workflow before opting in.
17. **Store.** `go test -tags integration ./adapters/store -run TestCancelActiveAssignmentsForExecution`, using `store.NewTestStore`:
    - an `active` assignment becomes `cancelled` with `responded_at` set, and the call returns 1;
    - a `completed` assignment of the same execution is untouched;
    - a second call returns 0.
18. **Standalone wiring.** A `service/cmd/spine` test asserts:
    - `stepEntryPolicyFromEnv` returns `StrictStepEntryPolicy()` for `""` and `strict`, and the zero value for `legacy`;
    - it errors for `foo`;
    - `buildOrchestrator(...).StepEntryPolicy()` and the orchestrator that `workspaceOrchestratorBuilder` stores on `ss.StepAssigner` both equal the env-selected policy.

    A gateway test asserts that with `DisableStepAssignFallback: true` and no `StepAssigner`, `POST /api/v1/runs/run-123/steps/step1/assign` returns 503 and the stub store records no `UpdateStepExecution`. Existing gateway assign tests, which use the fallback with the default `false`, pass unchanged.

    *Design-check note (2026-10-06):* the wiring test also asserts the scheduler hooks:

    - `buildScheduler(...)` installs `WithRunTerminated`;
    - `workspaceOrchestratorBuilder` sets `ss.RunTerminatedFn`;
    - under `legacy`, invoking either leaves executions unchanged.
19. **SMP is unaffected (plan acceptance criterion).** Use SMP main `80995559` with `go.mod`'s `replace github.com/bszymi/spine => ../spine` pointing at this branch's head, and no SMP change. These pass:
    - `go build ./cmd/... ./internal/...`;
    - `go vet ./internal/customer/...`;
    - `go test ./internal/customer/spineembedded/... ./internal/customer/spinecore/... ./internal/customer/run/...`.

    With `TEST_SPINE_DB` set, these pass too:
    - `TestAssignGate_Integration`, including "hybrid step to a human is unchanged" and "agent claims an ai_only entry step";
    - `TestRecoverAutomation_Integration` and `TestRecoverUnavailableOwner_Integration`, which cover the recovery `ClaimStep` paths.

    SMP's `AssignStep` on task-default execute (`artifact_status: Pending`) still succeeds; AC 16 pins the same case Spine-side.

    *Design-check note (2026-10-06):* add `go test ./internal/engine/...` to the hermetic list.
    The agent engine reaches `ClaimStep` and `ActivateStep` through `adapters.go:149` and
    `runner.go:280`.

**Repository gates.**

20. **No schema change.** `git diff 61994fe -- migrations/` is empty. No SMP migration is required.
21. **Spine gates pass:**
    - `go build ./...`, `go vet ./...` and `go test ./...`;
    - `go test -tags integration ./adapters/store/...` against a test database;
    - `go test -tags scenario ./service/scenariotest/...`, unchanged, because the harness uses the zero value;
    - `dev/lint-boundaries.sh`, with no new entry in `core_to_adapters_allowlist`;
    - `cmd/embed-smoketest`.
22. **Docs and changelog.** The documentation in Design §14 is updated, and `CHANGELOG.md` records the standalone behaviour changes and `SPINE_STEP_ENTRY_POLICY`. A reviewer can verify each listed section.

**Design-check additions (2026-10-06).** The probes are in `_work/task007/designcheck/`, run
with `go test -overlay .../designcheck/overlay.json ./core/engine/ -run 'TestT007_'` from
Spine. Each probe passes at `61994fe`, which demonstrates the gap. The committed tests below
invert it.

23. **`TestStepEntry_SoDAuthorLedger`** is the committed form of probe
    `TestT007_ReleaseErasesExecutorFromStepExecutions`. It uses the AC 7 design-check rows, with
    `memAssignmentStore` extended by `ListAssignmentsByActor`.
    - Under `SeparationOfDuties`, the released executor is refused on `review`.
    - Under the zero value, the same sequence binds them, exactly as the probe shows today.
    - *Mutation:* reading only `step_executions` fails it.
24. **`TestStepEntry_CandidateListingHidesDeadRuns`** is the committed form of probe
    `TestT007_CandidateListingOffersCancelledRunSteps`.
    - Under `EnforceRunStatus`, `ListStepExecutions` returns no execution of a `cancelled`,
      `failed`, `completed`, `committing` or `paused` run, and still returns the `active` run's
      execution.
    - Under the zero value it returns the cancelled run's execution, as today.
    - *Mutation:* deleting the run-status filter fails it.
25. **`TestStepEntry_LateSubmitOnTerminatedExecution`** is the committed form of probe
    `TestT007_LateSubmitOnTerminatedExecutionIsSuccess`.
    - Under `EnforceRunStatus`, `IngestResult` on a `skipped`/`run_terminated` execution returns
      `ErrConflict` naming the run status, and makes no store write.
    - A duplicate submit of a `completed` execution with the same outcome still returns the
      idempotent response with `nil` error.
    - Under the zero value, the terminated execution returns `status: skipped` with `nil`
      error, as today.
    - *Mutation:* routing the run-terminated case through the idempotent branch fails it.
26. **`TestScheduler_RunTimeoutTerminatesLiveSteps`** (`adapters/scheduler`).
    - With a fake store holding an `active` run past `timeout_at` and one `waiting` and one
      `assigned` execution, `ScanRunTimeouts` with `WithRunTerminated(fn)` calls `fn` exactly
      once with the run ID, after the run row is `cancelled`.
    - Driving it end to end with `orch.TerminateLiveSteps` under `TerminateStepsOnCancel`
      leaves both executions `skipped` with `run_terminated`.
    - When the timeout write is not applied (TASK-008's CAS miss, or a run already terminal or
      `committing`), `fn` is not called.
    - *Mutation:* dropping the `fn` call fails it.
27. **`TestStepEntry_ResolverContract`.**
    - A resolver that returns an `Actor` whose `ActorID` differs from the request (for example a
      case variant) makes `AssignStep` and `ClaimStep` return `ErrNotFound`, with nothing
      written.
    - A resolver returning `domain.NewError(ErrUnavailable, …)` surfaces as `ErrUnavailable`,
      not `ErrInternal`.
    - An untyped resolver error surfaces as `ErrInternal`.
    - A gating policy on an `&Orchestrator{}` with nil `wfLoader` returns `ErrUnavailable` from
      `AssignStep` instead of panicking.
    - *Mutation:* removing the `ActorID` equality check fails the first sub-case.

## Out of scope / follow-ups

- **Submit-side ownership, required-output evidence and compare-and-swap writes** (P3-17, P3-19, P3-24): TASK-008. This task only adds the run-status guard to `IngestResult` and `SubmitStepResult`.
- **Skill self-grant and the admin-only skill API** (F6, P3-33): TASK-013. `RequireSkillsOnAssign` becomes a real boundary in standalone once that lands.
- **The F3 merge-path governance guard:** TASK-006.
- **Recording the run initiator for SoD:** needs a `runtime.runs` column, a schema change. Follow-up if owner question 1 asks for it.
- **An exclusion list on `actor.SelectionRequest`,** so auto-selection can pick a non-author reviewer instead of leaving the step waiting.
- **Terminating divergence contexts and branches** (`runtime.divergence_contexts`, `runtime.branches`) when a run ends. Only step executions and assignments are closed here.
- **Updating `ExecutionProjection`** (`AssignedActorID` / `AssignmentStatus`) on cancel. `CancelRun` does not touch it today either.
- **Promoting the deferred-status lint into `workflow.Validate`,** and making strict precondition evaluation the default for every embedder, once SMP has opted in and the `legacy` escape hatch is retired.
- **The `artifact_status` doc example** in `workflow-definition-format.md` §5.1 uses an `artifact:` key, while the engine reads `path`. That is a pre-existing doc mismatch; it is noted, not fixed here.
- **SMP's own adoption** (actor resolver, workflow fixes and backfill, the SoD decision for solo workspaces): SMP TASK-011. The `.spine-revision` bump is SMP TASK-012.
- *(Design check.)* **An append-only binding ledger** (Owner question 6) would close the SoD
  residual for `AssignStep` bindings reset by restart recovery. It is a schema change.
- *(Design check.)* **A one-shot sweep of live executions on runs that ended before the
  upgrade.** They are inert under `EnforceRunStatus` and hidden by the listing rule. A sweep is
  cosmetic and operational, not a security fix.
- *(Design check.)* **`RetryAfter` on bind.** `AssignStep` and `ClaimStep` do not honour a
  retry execution's backoff. SMP checks it for runner steps (`planRunAndMode`). It is not in the
  audit's findings, so this task does not add it.
- *(Design check.)* **`actor.Gateway.ProcessResult`** (`core/actor/gateway.go:80-130`) completes
  an execution with no state machine and no run check. It has no production caller in Spine or
  SMP, so it is dormant. It is noted here because it is the one store-level completion path the
  §9 guards do not cover; TASK-008 already lists it.
- *(Design check.)* **Mode versus declared types.** `workflow.Validate` does not reject a mode
  that contradicts `eligible_actor_types` (for example `human_only` with `[ai_agent]`;
  `core/workflow/parser.go:244-290`). Under this policy both check 3 and check 5 must pass, so
  such a step is unbindable. A validation rule is a follow-up for TASK-014 (workflow governance).

## Design check

An adversarial review on 2026-10-06 against Spine `61994fe` and SMP `80995559`, read-only on both
repositories (both trees verified clean afterwards). Probes are under
`_work/task007/designcheck/`. The designer's text is kept. Every change is a marked
"Design-check note (2026-10-06)", a "design check" decision (14-17), an Owner question (5-7) or an
AC addition (rows in AC 2, 5 and 7, notes on AC 1, 11, 12, 15, 18 and 19, and new AC 23-27).

**Bypass found: separation of duties forgot released executors.**

- Rebinding reuses the execution row. `ReleaseStep` blanks `ActorID` (`release.go:72-77`), and
  restart recovery plus a rebind overwrites it.
- So "any execution's `ActorID`" missed an executor who pushed, released and let someone else
  finish, and that executor could then self-review. Probe `TestT007_ReleaseErasesExecutorFromStepExecutions`
  passes at HEAD.
- **Added:**
  - the author ledger now includes `actor_assignments` in any status, read through the existing
    `ListAssignmentsByActor`, which fails closed;
  - a symmetric guard for divergence branches;
  - D14, the AC 7 rows, AC 23 and Owner question 6 (the residual for assign-bound rows, which
    needs a ledger).

**Coverage gaps in P3-16 closed.**

- **The scheduler's run-timeout cancel** (`run_timeout.go:61`) bypasses `CancelRun`, so timed-out
  runs kept live executions.
  - **Added:** a `WithRunTerminated` hook plus the exported `TerminateLiveSteps`, wired in
    `buildScheduler` and the workspace `ServiceSet`; D17 and AC 26.
- **The candidate listing** (`step_executions.go`) offered dead runs' steps to pollers, which
  would have answered 409 on every cycle.
  - **Added:** a run-status filter under `EnforceRunStatus`; D16 and AC 24.
- **A late submit on a terminated execution** answered `200 skipped` through the idempotent
  branch, breaking SMP's F12 `409 RUN_NOT_ACTIVE` contract. SMP TASK-011 found this
  independently.
  - **Added:** `ErrConflict` under `EnforceRunStatus`; D15 and AC 25.
  - **Coordinate with TASK-008 §3a**, which touches the same branch.

**Claims corrected.**

- "Only three paths into failed or cancelled": true of `core/engine` only (§10 note).
- "Released-and-rebound attempt still counts": false at HEAD (§7 note).
- §15 "backfill through INIT-020 seeding": the backfill is add-if-never-existed, so SMP TASK-011
  uses a load-time legacy loader instead.
- The SMP caller inventory was missing five call sites (§15 note).
- The P3-16 SMP cell omitted SMP's existing F12 submit and acknowledge gates, which refuse only
  terminal runs (note after the threat table).
- The "prototype confirmed" claims could not be re-run, because no prototype was kept (§13
  note). All seven AC-1 PoCs were re-run and pass together at HEAD.

**Contract tightening for consumers.**

- **Resolver:** exact `ActorID` echo, so aliases cannot defeat exact-string SoD or ownership
  compares; typed errors pass through; a nil `wfLoader` does not panic. This is AC 27.
- **`DeferredStatusPrecondition`** gets a pinned shape (`StepID`, `Index`, `Type`, `Status`,
  `SetBy`), because SMP TASK-011's legacy loader removes exactly what it reports (AC 15 note).
- **Allow-list widening** by the assigner is documented as intended (§3 note, AC 2 row, Owner
  question 5).
- **`TerminateStepsOnCancel`** is documented as safe only with `EnforceRunStatus`, because
  termination races a concurrent blind bind write (P3-24) until TASK-008's CAS. Setting it alone
  logs a `WARN`.

**Test hygiene.** The committed legacy pins (AC 1) keep only binding assertions. Otherwise
TASK-005 (SHA-bound merge) and TASK-008 (submit identity, CAS) would turn this task's tests red.

**Checked and found sound.**

- The zero-value guarantee.
- Every Spine file:line reference sampled: `assign.go`, `claim.go`, `step.go:724-878`,
  `947-956`, `parser.go:75-78`, `permissions.go:34/47`, `run.go:452-516`, `merge.go:443-466`,
  `handlers.go:163-241`, the migrations and the `serve` wiring.
- That `ActivateStep` is never reached on a `committing` run by engine code: commit retry calls
  `MergeRunBranch` directly (`serve_builders.go:313-315`).
- That `PauseRun` has no production caller.
- SMP's mode gates, recovery claim and `ai_only` handling against the mode table.
- That SMP's projection loader refuses a version mismatch (`spinecore/workflow_loader.go:115-118`),
  so a mid-run workflow edit cannot retype a review step to dodge SoD; it fails closed.
- That SMP wires the discussions checker and validator (`spinecore/factory.go:178-179`), so
  strict fail-closed precondition evaluation does not strand SMP's corpus.
- That no Spine or SMP workflow uses `discussions_resolved`, `cross_artifact_valid` or `custom`.

## Implementation Notes

