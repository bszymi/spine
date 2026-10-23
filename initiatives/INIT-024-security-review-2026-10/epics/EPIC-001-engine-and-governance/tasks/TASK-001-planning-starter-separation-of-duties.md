---
id: TASK-001
type: Task
title: "Separation of duties counts a planning run's starter as an author"
status: In Progress
epic: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-001-engine-and-governance/epic.md
initiative: /initiatives/INIT-024-security-review-2026-10/initiative.md
work_type: security
created: 2026-10-23
last_updated: 2026-10-23
links:
  - type: parent
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-001-engine-and-governance/epic.md
  - type: blocks
    target: /initiatives/INIT-024-security-review-2026-10/epics/EPIC-005-close-out/tasks/TASK-010-initiative-close.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-007-step-entry-guards.md
  - type: related_to
    target: /initiatives/INIT-023-security-audit-remediation/epics/EPIC-002-governance-integrity/tasks/TASK-006-governance-path-guard.md
  - type: related_to
    target: /architecture/adr/ADR-006-planning-runs.md
---

# TASK-001 — Separation of duties counts a planning run's starter as an author

---

## Purpose

This task closes **N-3** from the 2026-10-22 review.

- The finding is also catalogued as GV-1 and SA-1. Detail and PoCs are in SMP's git-ignored
  `tmp/security-review-2026-10-22/spine/REPORT.md`.
- **Priority:** this task is wave 1. It merges to Spine `main` before the INIT-023 closing
  commit, so SMP EPIC-004 TASK-012's closing bump carries it (initiative owner question 2).
- It is small and self-contained: no new option, no API change, no migration.

## Findings addressed

| ID | Sev | Reach | Precondition |
|---|---|---|---|
| N-3 | P3 | **Engine** (in SMP's pin). Reachable only with `StepEntryPolicy.SeparationOfDuties` on. `spine serve` has it on (strict policy). SMP has it off (SMP TASK-011 owner question 2). | An actor who may start a planning run (`POST /artifacts` planning mode, or SMP `CreateEntry`) and who is eligible for the creation workflow's review step. |

**The defect.**

- A planning run writes the starter's content onto the run branch at start.
- Separation of duties defines a run author only from step executions and the assignment
  ledger.
- The starter is in neither, so nothing stops them reviewing their own content.
- An AI or automated draft step that submits the content unchanged, as the PoC does, then
  produces a run whose only human is both author and reviewer. Its content reaches `main`.

## Current code (verified at `a560c210`)

- **`core/engine/run.go:213` `StartPlanningRun`.** It reads no actor. It builds the run (`:273-290`),
  and `startRunCommon` persists it (`:63`) before `onBranch` writes `artifactContent` (`:296`).
  Nothing records who started it.
- **`adapters/store/postgres_runs.go:34-41`.** `CreateRun` does not persist `commit_meta`, so a
  starter set on the `domain.Run` literal would be lost. Any record must go through
  `mergeRunCommitMeta` (`core/engine/governance_guard.go:883`) after `CreateRun`.
- **`core/engine/governance_guard.go:190-198`.** `CommitMetaGovernanceStartedBy`
  (`spine.governance.started_by`) already exists, but only for workflow-lifecycle runs.
  - `recordLifecycleStarter` (`:820-833`) writes it only while the guard **and** the lifecycle
    exemption are on.
  - It is called from `startWorkflowPlanningRun` (`run.go:470`).
  - With no actor on the context, or on a write error, it records nothing.
- **`core/engine/step_entry.go:932` `runAuthor`.** It counts:
  - (a) actors bound to author executions;
  - (b) assignment-ledger rows.

  It never reads `run.CommitMeta`. Both bind paths reach it through `separationOfDuties`
  (`:881`), and so does the candidate listing (`step_executions.go:158`, `sodHidesCandidate`
  `:196`).
- **`governance_guard.go:796-800` `reviewerIsNotAuthor`.** It already compares the starter, but
  only for the lifecycle exemption. That is why the guard holds lifecycle runs while separation
  of duties does not refuse the bind.
- **Callers.**
  - The standalone gateway puts the authenticated actor on the context
    (`service/internal/gateway/middleware.go:244`) before `handlers_workflow.go:92` and
    `handlers_artifact_entry.go:116`.
  - SMP `CreateEntry` (`internal/customer/spineembedded/artifacts_createentry.go:118, :186` at SMP
    `3addd44b`) bridges only `observe` actor fields, not `domain.WithActor`.

## Design

1. **One starter record for every planning-mode run.**
   - Generalise `recordLifecycleStarter` into
     `recordPlanningStarter(ctx, runID string) error`, called from both planning starters:
     - in `StartPlanningRun`'s `onBranch`, **before** `artifactWriter.Create`;
     - in `startWorkflowPlanningRun`, where it is called today, but moved before `write`.
   - It writes `CommitMetaGovernanceStartedBy` = `domain.ActorFromContext(ctx).ActorID` when
     `o.entryPolicy.SeparationOfDuties` is on, or when the guard and the lifecycle exemption are on
     (today's condition). Otherwise it writes nothing, so the zero value makes no store call.
   - The key keeps its name (Decision 1). Its doc comment changes from "workflow-lifecycle run" to
     "planning-mode run".
2. **Fail closed at start under separation of duties.**
   - With `SeparationOfDuties` on and no actor (or an empty `ActorID`) on the context, both
     planning starters return `ErrUnauthorized` ("a planning run under separation of duties needs
     an authenticated starter"). The check runs **before** `startRunCommon`, so no row and no
     branch exist.
   - If the record write itself fails, `onBranch` returns the error before any content is written.
     This uses the existing cleanup path: branch deleted, run row reconciled by recovery.
   - With only the lifecycle exemption on, today's "record nothing, so no exemption" stays.
3. **`runAuthor` counts the starter.**
   - For a run with `Mode == domain.RunModePlanning`, before scanning executions:
     - if `run.CommitMeta[CommitMetaGovernanceStartedBy] == actorID`, return the pseudo-step
       `runStartAuthorStep = "(run start)"`;
     - if the key is absent, return `planningStarterUnknown = "(run start: starter not recorded)"`.
       Every candidate is then refused (Decision 2, fail closed).
   - Standard runs are unchanged: their starter wrote no content.
   - The refusal text in `separationOfDuties` (`step_entry.go:891`) already prints the step id.
     `"(run start)"` reads as "executed step (run start)", and the message is reworded for that
     case.
4. **Nothing else changes.** `sodHidesCandidate` and `reviewerIsNotAuthor` inherit the change.
   The separate starter check in the latter stays as defence in depth.

## SMP impact

- **Inert at the bump** while SMP keeps separation of duties off. The zero value writes nothing
  and refuses nothing.
- **When SMP turns it on** (SMP TASK-011 owner question 2), `CreateEntry` must attach
  `domain.WithActor(ctx, &domain.Actor{ActorID: <member handle>})` before `StartPlanningRun`.
  - The handle must be the exact string SMP's step-entry resolver later binds (`human-<uuid>`),
    because the comparison is exact.
  - Without it every planning start is refused with `ErrUnauthorized` (fail closed).
  - Since review round 1, a handle that SMP's `StepEntryPolicy.Actors` resolver does not resolve
    is refused too (`ErrUnauthorized`). Recording the platform user id would otherwise silently
    reopen N-3.
- *Correction (review round 1).* An earlier draft said this refusal was "visible in SMP's tests".
  **It is not, today.** SMP TASK-011's only test with separation of duties on uses standard runs.
  SMP's scenario harness always binds an actor. `CreateEntry` reaches `StartPlanningRun` with
  separation of duties off. Nothing in SMP's suites would turn red if the wiring below were
  missing, so SMP must add the test in obligation 4.
- **SMP obligations** before `SMP_STEP_SEPARATION_OF_DUTIES` is turned on with this Spine. They are
  for SMP EPIC-004 TASK-012's closing bump; detail is in the review record
  `tmp/init027-orchestration/i24/t001-rev/REVIEW.md`.
  1. **`embeddedArtifacts.CreateEntry` binds the workspace step-entry handle.**
     - Before `resolveCreateEntryTarget` / `StartPlanningRun`, resolve
       `ResolveSpineActorID(ctx, sc.WorkspaceID, auth.ActorIDFrom(ctx))`. That is
       `platform.workspace_roles.spine_actor_id`, `human-<uuid>`, the same resolver as
       `execution.Handler.resolveActorID` / `AutoAssignEntryStep`.
     - Then set `ctx = domain.WithActor(ctx, &domain.Actor{ActorID: handle})`.
     - Never use `auth.ActorIDFrom` or `observe.ActorID`: the platform UUID would reopen N-3. The
       engine now refuses that, since it does not resolve.
     - Leave `Role` at its zero value.
     - A missing handle must fail **before** the call (403, like `ErrNoActorMapping`), so that
       proposal promotion classifies it as pre-run. The engine's own early 401 comes back from
       `starter.StartPlanningRun` and is wrapped as `CreateEntryError{RunStarted:true}`
       (`artifacts_createentry.go:186-188`). Promotion would then mark the proposal
       `failed_stranded` although no run row exists.
  2. **Every orchestrator that starts planning runs carries the same separation-of-duties flag.**
     TASK-011's `cache.Options()` does.
  3. **In-flight SMP planning runs have no record.** With separation of duties on, nobody can
     review them (Decision 2). Drain or cancel them before the switch, or backfill
     `spine.governance.started_by` once with the exact handle.
  4. **Add an SMP test with separation of duties on.** Run `CreateEntry`, then:
     - the starter's assign or claim of the review returns 403;
     - a colleague's assign or claim succeeds;
     - an unmapped member's `CreateEntry` fails before the run, as pre-run.
  5. **Residuals to record on the SMP side:**
     - TASK-011 OQ9: a re-added member gets a new handle, and so is not the recorded starter.
     - Admin overwrites through `WriteContent` / `AddToRun` need no binding, so an admin can write
       and then review. This is SMP's version of Spine's operator residual.
- SMP TASK-011 / TASK-012 record this task as "adopted here" in the closing bump's ledger.

## Decisions

1. **Reuse `spine.governance.started_by`.** It is already reserved, kept across bindings by
   TASK-005, and read by the lifecycle exemption. A second key would let the two disagree.
2. **A planning run with no recorded starter is reviewable by nobody while separation of duties
   is on.** This covers runs started before the upgrade, or started while the policy was off. The
   operator cancels and restarts the run, or sets `SPINE_STEP_ENTRY_POLICY=legacy` for the window.
   Recorded in `KNOWN-LIMITATIONS.md` and `CHANGELOG.md`.
3. **The actor comes from `domain.ActorFromContext` only**, as for `recordLifecycleStarter`.
   `observe.ActorID` is a logging value and is not used for authority.

## Owner questions

1. **Unrecorded starters.** *Default:* fail closed (Decision 2). *Alternative:* grandfather
   them, so a missing key means today's rule. Pick the alternative if any standalone deployment
   has open planning runs at upgrade time.
2. **Error class for "no starter".** *Default:* `ErrUnauthorized` (401). *Alternative:*
   `ErrUnavailable` (503, a wiring error).

## Acceptance Criteria

Every test named "regression" fails at `a560c210` and passes on the task branch. The
Implementation Notes record both runs.

1. **Regression: the starter cannot review**
   (`core/engine`, `TestSoD_PlanningStarterCannotReviewOwnContent`).
   - This is a port of `TestGV_SoD_PlanningStarterSelfReview`. Under `StrictStepEntryPolicy()`, a
     planning run is started with actor `starter` on the context, and `bot` completes `execute`.
   - `ClaimStep` and `AssignStep` of `review` for `starter` each return `ErrForbidden` naming
     `(run start)`.
   - `main` never gains the artifact. At `a560c210` the claim succeeds and the accept merges.
2. **Regression: the gateway path** (`service/scenariotest`,
   `TestScenario_PlanningRunStarterCannotSelfReview`).
   - This is a port of `scratch/standalone/sa1` (`TestZZSA1_PlanningRunStarterIsNotAnAuthor`):
     `POST /artifacts` in planning mode, then the same token claims the review.
   - Expected: `403`.
3. **The record** (`TestSoD_PlanningStarterRecorded`). After `StartPlanningRun` under separation
   of duties, `commit_meta["spine.governance.started_by"] == "starter"`. The same holds for
   `StartWorkflowPlanningRun` and `StartWorkflowUpdatePlanningRun` with the guard off.
4. **Zero value** (`TestSoD_ZeroPolicyWritesNoStarter`). With no policy and no guard, the fake
   store sees no `SetCommitMeta`/`MergeCommitMeta` call during the start, and an actor-less
   context starts the run as before.
5. **No actor, no run** (`TestSoD_PlanningStartWithoutActorRefused`). Under separation of duties,
   an actor-less context returns `ErrUnauthorized`, no run row exists, and no `spine/plan/*`
   branch exists.
6. **Record failure aborts before content** (`TestSoD_PlanningStarterRecordFailureAbortsStart`).
   A store whose `MergeCommitMeta` fails makes the start fail, the branch is deleted, and the
   artifact writer is never called.
7. **Unrecorded starter fails closed** (`TestSoD_PlanningRunWithoutStarterRecordRefusesReview`). A
   planning run with no key refuses every candidate for `review` with `ErrForbidden`.
8. **Others still review** (`TestSoD_IndependentReviewerAccepts`). A third actor claims, accepts,
   and the merge lands.
9. **The listing** (`TestSoD_CandidateListingHidesPlanningReviewFromStarter`). With an `actor_id`,
   `ListStepExecutions` hides the waiting review from `starter` and shows it to the third actor.
10. **Standard runs unchanged** (`TestSoD_StandardRunStarterIsNotAnAuthor`). The starter of a
    `StartRun` run may review it, as today.
11. **Existing suites.**
    - `go test ./core/engine/... ./service/...` passes.
    - The scenario suite passes, except the known pre-existing failures identical on `main`
      (INIT-023 §7f).
    - Scenario steps that start planning runs under the strict policy put an actor on the
      context, as `lifecycleStartCtx` (`service/scenariotest/engine/workflow_steps.go:86`) does.
12. **SMP compatibility gate** (initiative §5) passes with no SMP change.
13. **Documentation.**
    - `security-model.md` §4.4: the author definition includes the planning starter.
    - `KNOWN-LIMITATIONS.md`: Decision 2.
    - `CHANGELOG.md` (standalone): a planning start without an actor under the strict policy is
      `401`; an in-flight planning run's review is refused.

## Compatibility

- There is no API, interface or schema change. `StartPlanningRun`'s signature is unchanged,
  so SMP's `artifacts_createentry.go:22` interface still matches.
- Standalone behaviour changes (strict policy on):
  - a planning run's starter can no longer review it;
  - an actor-less planning start is refused;
  - pre-upgrade planning runs cannot be reviewed (Decision 2).

## Out of scope

- Counting the starter of a standard run, or the author of the task file on `main`.
- An append-only binding ledger for assign-bound executions reset by recovery. That is the
  INIT-023 TASK-007 owner question 6 residual.

## File overlaps

- `core/engine/governance_guard.go`: this task touches `recordLifecycleStarter` (`:820`);
  TASK-002 touches `buildGovernanceCheck` (`:499`).
- `core/engine/run.go` and `core/engine/step_entry.go`: this task only.

## Implementation Notes

Branch `init/INIT-024-TASK-001-planning-starter-sod`, cut from `init/INIT-024-scaffold` `12376b55`
(= Spine local `main` `a560c210` + the INIT-024 scaffold; no Go file differs from `a560c210`). Diff
base: `git diff init/INIT-024-scaffold...HEAD`. Scratch (git-ignored, SMP working tree):
`tmp/init027-orchestration/i24/t001/`.

### Owner questions (defaults recorded, pending the owner)

- **OQ1 — unrecorded starters:** default taken, **fail closed** (Decision 2). A planning run with
  no `spine.governance.started_by` is reviewable by nobody while separation of duties is on.
- **OQ2 — error class for "no starter":** default taken, **`ErrUnauthorized`** (401).
- INIT-024 owner question 2 (land before INIT-023's close) is the orchestrator's call; nothing here
  depends on it.

### What was built

- **`core/engine/governance_guard.go`.**
  - `recordLifecycleStarter` became `recordPlanningStarter(ctx, run *domain.Run) error`. It writes
    `CommitMetaGovernanceStartedBy = domain.ActorFromContext(ctx).ActorID` (through
    `mergeRunCommitMeta`) when `SeparationOfDuties` is on, or when the guard and the lifecycle
    exemption are on. Otherwise it makes no store call.
    - Under separation of duties, a missing actor is `ErrUnauthorized` and a failed write is
      returned.
    - With only the exemption on, nothing changes: no actor or a failed write records nothing and
      the start goes on.
    - On success the record is mirrored into the returned run's `CommitMeta`.
  - New `requirePlanningStarter(ctx)`: under separation of duties, a nil actor or an empty
    `ActorID` is `ErrUnauthorized`. The key's doc comment now says "planning-mode run". The
    `mergeRunCommitMeta` comment notes the new pending-run writer.
- **`core/engine/run.go`.**
  - `StartPlanningRun` and `startWorkflowPlanningRun` call `requirePlanningStarter` first, right
    after the writer-nil wiring check. That is before parse, `ResolveWorkflowForMode`,
    `workflowPlanningPreCheck` (a git read) and `startRunCommon`.
  - Both `onBranch` callbacks call `recordPlanningStarter` before the content write. For the
    workflow starter that moves the call from after `write` to before it.
  - A record failure deletes the branch (local, and remote with auto-push) and marks the pending
    run failed (`failOrphanPlanningRun`).
  - The duplicated branch cleanup became `deletePlanningBranch`. The log messages are unchanged.
  - `failOrphanWorkflowRun` was renamed `failOrphanPlanningRun`. Its WARN now reads "planning run
    start failed; could not mark the run failed" (CHANGELOG).
  - The generic planning run's artifact-write failure path is byte-for-byte as before: branch
    deleted, row left for recovery. SMP's `CreateEntry` documents and relies on that.
- **`core/engine/step_entry.go`.**
  - `runAuthor`: for `Mode == planning`, before anything else, an empty or missing record returns
    `planningStarterUnknown = "(run start: starter not recorded)"`, and `starter == actorID`
    returns `runStartAuthorStep = "(run start)"`.
  - `separationOfDuties` words the two refusals separately; both are `ErrForbidden`. The first
    names the actor and `(run start)`. The second names no actor, only the run and the
    remedy.
  - `sodHidesCandidate` and `reviewerIsNotAuthor` inherit the change. `reviewerIsNotAuthor` still
    checks the starter itself first, so its behaviour is unchanged.
  - Field and function docs were updated.
- **`core/engine/commitmeta.go`** (see Deviation 3). With merge binding off, a commit-carrying
  outcome's whole-map `SetCommitMeta` keeps `spine.governance.started_by`.
- **Scenario harness** (`service/scenariotest/engine/workflow_steps.go`). `lifecycleStartCtx` was
  renamed `planningStartCtx` and now also wraps `engine.StartPlanningRun` (AC 11).
- **Tests.**
  - `core/engine/planning_starter_sod_test.go`: 14 tests.
  - `service/scenariotest/scenarios/planning_starter_sod_test.go`: 3 scenarios.
  - `workflow_lifecycle_starter_test.go`: one new scenario and one re-scoped scenario (Deviation 6).
- **Docs.** `architecture/security-model.md` §4.4.1, `architecture/actor-model.md` §7.3,
  `docs/integration-guide.md` (`SPINE_STEP_ENTRY_POLICY` row), `KNOWN-LIMITATIONS.md` (Decision 2
  and its sub-bullets) and `CHANGELOG.md` (Security, Changed (standalone), Changed (embedders)).

### Per-AC evidence

Red = the AC tests compiled into a `git archive` of `init/INIT-024-scaffold` (`t001/base/`,
command `go test ./core/engine/ -count=1 -v -run 'TestSoD_|TestPlanningStarterRecord_|TestMergeCommitMeta_BindingOffKeepsPlanningStarter'`,
log `t001/red-engine-base.log`; scenarios via `t001/scen.sh <tree> <tag> 'TestScenario_'`, log
`t001/scen-redbase.log`). Green = the same on the branch (`t001/green-engine-branch.log`,
`t001/scen-brtarget.log`, `t001/scen-brfull.log`). The test files compile at the base because they
name the pseudo-steps as strings, not as the new constants.

| AC | Test | Base (red) | Branch |
|---|---|---|---|
| 1 regression | `TestSoD_PlanningStarterCannotReviewOwnContent` (real git, strict policy, `StartPlanningRun` as `starter`, AI `bot` completes `execute`) | FAIL: "N-3: the starter claimed the review of its own planning content (accept: err=<nil>, run completed, main has the artifact: true)" | PASS: claim and assign both `ErrForbidden` naming `(run start)`; review untouched; `main` lacks the file |
| 2 regression | `TestScenario_PlanningRunStarterCannotSelfReview` (`POST /runs` mode=planning, then the same token `POST /execution/claim`) and `TestScenario_ArtifactEntryStarterCannotSelfReview` (`POST /artifacts/entry`) | FAIL: claim = **200** (`"status":"claimed"`) | PASS: **403** containing `(run start)`; the listing hides the review from the starter and shows it to `pl-reviewer`, who approves; the artifact reaches `main`; the record is `pl-starter` |
| 3 | `TestSoD_PlanningStarterRecorded` (3 starters × MergeCommitMeta / read-modify-write, SoD on, guard off) | FAIL ×6: stored key `""` | PASS: stored and returned `starter`; exactly one write |
| 4 | `TestSoD_ZeroPolicyWritesNoStarter` (3 starters × actor / no actor) | PASS (pins the zero value) | PASS: 0 `SetCommitMeta` and 0 `MergeCommitMeta`, run active, one content write |
| 5 | `TestSoD_PlanningStartWithoutActorRefused` (3 starters × no actor / empty id, strict) | FAIL ×6: nil error | PASS: `ErrUnauthorized`; no row, no branch, no git read, no content write |
| 6 | `TestSoD_PlanningStarterRecordFailureAbortsStart` (3 starters × both write paths failing) | FAIL ×6: nil error | PASS: the record error is returned; writer never called; branch created then deleted; row `failed`; no entry step |
| 7 | `TestSoD_PlanningRunWithoutStarterRecordRefusesReview` (4 metadata shapes × 3 actors; plus auto-selection) | FAIL: assigns succeed or refusals do not name the unrecorded starter; auto-selection binds `agent-2` | PASS: every assign and claim `ErrForbidden` naming `(run start: starter not recorded)`; auto-selection leaves the review waiting; the zero value still binds |
| 8 | `TestSoD_IndependentReviewerAccepts` (real git) | the accept and merge pass; the record assertion fails | PASS: `reviewer` claims, accepts, run completed, artifact on `main`, record kept after the merge |
| 9 | `TestSoD_CandidateListingHidesPlanningReviewFromStarter` | FAIL: listing offers `starter` the review | PASS: hidden from `starter` and the drafter, shown to `reviewer`; an unrecorded run's review is offered to nobody |
| 10 | `TestSoD_StandardRunStarterIsNotAnAuthor` | PASS (unchanged behaviour) | PASS: `StartRun` writes no record; an actor-less standard start works; a standard run's starter may review (even with a stray record) |
| 11 | `go test ./...` and the scenario suite (below) | — | PASS; scenario failures identical to the base (31 known, INIT-023 §7f) |
| 12 | SMP compatibility gate (below) | — | PASS with no SMP change |
| 13 | Docs as listed above | — | Done |

Extra tests from the self-review, all red at the base except where noted:

- `TestSoD_RecordedStarterRefusedOnEveryBindPath`: assign, claim and auto-selection.
  - The drafter is still refused by its own execution.
  - Under SoD alone, case and whitespace variants of the id are other ids (the compare is exact).
  - Under the strict policy such variants do not resolve (`ErrNotFound`).
- `TestSoD_EveryPlanningStarterIsRefusedItsReview`: for all three starters, the record each one
  writes is what `runAuthor` reads.
- `TestPlanningStarterRecord_GuardOnlyKeepsBestEffort`: the guard-only behaviour is unchanged for
  the lifecycle starters. The generic starter's "recorded" sub-case is new behaviour (Deviation 5).
- `TestPlanningStarterRecord_BeforeContent`: the key is in the store when the writer runs.
- `TestMergeCommitMeta_BindingOffKeepsPlanningStarter`.
- `TestScenario_PlanningStartWithoutActorRefusedUnderStrictPolicy`.
- `TestWorkflowLifecycle_GatewayStartedRunStarterCannotClaimReview`: not run at the base. The
  base copy of `…ReviewedByItsStarterIsHeld` passes there, with the starter claiming under the
  strict policy, which is the behaviour this test now refuses.

### Deviations (with reasons)

1. **AC 2: "`POST /artifacts` in planning mode" does not exist.** `POST /artifacts` writes only
   (`handlers_artifacts.go`). The planning starts are `POST /runs` with `mode: planning`, which
   the sa1 PoC describes, and `POST /artifacts/entry`. Both are tested, with the AC's test name
   on the first.
2. **`recordPlanningStarter` takes the `*domain.Run`, not a run id,** so the start's returned run
   carries the record. The sa1 PoC inspects `res.Run.CommitMeta`. Same store call.
3. **`mergeCommitMeta` keeps the starter with binding off (`commitmeta.go`, not in the plan).**
   - With merge binding off, a commit-carrying outcome replaces the whole map and drops every
     reserved key.
   - A custom creation workflow with a `commit:` on a draft outcome would therefore erase the
     record before review. The run would then be unreviewable by everyone: fail closed, but
     stranded.
   - Only that one key is carried. It is written only under separation of duties or the guard,
     so for SMP (neither) the function is identical for every input. `spine serve` always has
     binding on (`serve_builders.go:353`).
4. **The new record-failure path also marks the pending run failed.** The design said "branch
   deleted, run row reconciled by recovery". Recovery would activate a branchless run that nobody
   can review. The path is reachable only under separation of duties, so it is inert for SMP.
   The pre-existing artifact-write failure path is unchanged.
5. **The guard-only record now also covers generic planning runs,** as the design's single
   condition says. It is inert: the exemption reads the key only for lifecycle runs. It keeps such
   runs reviewable if separation of duties is turned on later. Documented in `KNOWN-LIMITATIONS.md`.
6. **`TestWorkflowLifecycle_GatewayStartedRunReviewedByItsStarterIsHeld` now runs with separation
   of duties off** (`lifecycleActorsOnly`). Its premise, that the starter may claim under the
   strict policy, is exactly what this task removes. It still pins the guard's own starter check
   (defence in depth). The new `…StarterCannotClaimReview` covers the strict policy.
7. **The actor check runs first, not "just before `startRunCommon`".** No row and no branch exist
   either way, but running first also avoids git reads. In particular,
   `workflowPlanningPreCheck` would otherwise tell an actor-less caller whether a workflow exists.
   Consequence: under the policy an actor-less call with bad parameters gets 401, not 400.

### Adversarial self-review (what was tried; all closed or recorded)

- **Claim, assign, auto-selection and listing.** All four reach `runAuthor` through
  `separationOfDuties`. Tested: AC 1, 7, 9 and `…RefusedOnEveryBindPath`.
  - `sodBindSection` re-checks with the caller's run, which already carries `commit_meta` (Postgres
    `runColumns`).
  - `GET /execution/candidates` lists tasks, not executions, so it is not a binding listing.
- **Actor-id spelling.**
  - The record is the authenticated actor. Claim requires `req.ActorID ==` the authenticated id
    (`handlers_claim.go`). Assign targets resolve exactly (`checkResolvedActor`).
  - A variant is a different id and cannot submit as the starter. Embedder resolvers must stay
    canonical (existing `KNOWN-LIMITATIONS.md` entry).
- **Runs started before the record exists.**
  - Pre-upgrade runs and runs started with the policy off: fail closed (AC 7).
  - Within a start, the record is written after `CreateRun` and before the entry step and the
    content, so no review execution can exist before it.
  - If scheduler recovery activates the pending row in that window, it creates only the entry
    (author) step.
  - A record whose CAS-to-failed loses to recovery leaves an active run that nobody can review
    (fail closed).
- **Record erasure.**
  - Workflow outcomes cannot write `spine.*` keys (stripped, case-insensitively).
  - The engine rebinds keep reserved keys (`withEngineBinding`).
  - Binding-on outcome writes keep reserved keys.
  - Binding-off writes now keep this key (Deviation 3).
  - No gateway route writes `commit_meta`.
  - Erasure would in any case fail closed.
- **Workflow-lifecycle variants (create and update).** Same record and same refusal:
  `…EveryPlanningStarterIsRefusedItsReview`, AC 3/5/6 with three starters each, and the gateway
  lifecycle scenario.
- **Other content writers on a planning branch** (`POST /artifacts/add`, `write_context`).
  TASK-014 limits these to the actor bound to the current step, who is an author by execution, or
  an operator. An operator who writes and then reviews is the existing operator residual
  (`KNOWN-LIMITATIONS.md` "Operators push unbound…"). Not N-3, not changed.
- **A review already bound to its starter before the upgrade stays bound.** Separation of duties
  is a bind-time check. Recorded in `KNOWN-LIMITATIONS.md` (operator: cancel, or release and
  re-offer). Not closed: a submit-time check would be new enforcement outside this task.

### Gates (commands and results)

Spine (worktree at `79ed5e48`, then `42b7e4c4`; scripts and logs in `t001/`):

- `t001/basic.sh`:
  - `./dev/lint-boundaries.sh` rc=0.
  - `make build` rc=0.
  - `go build -o bin/embed-smoketest ./cmd/embed-smoketest && ./bin/embed-smoketest` rc=0.
  - `go vet ./...`, `-tags integration ./...` and `-tags scenario ./...` all rc=0.
  - `go test -p 4 -count=1 ./...` rc=0, so the known `adapters/store` and LocalStack failures did
    not trigger without a database.
- `go test -race -p 4 -count=1 ./core/engine/ ./service/scenariotest/engine/` rc=0.
  `t001/race-scen.sh` (`-race -tags scenario -run 'TestScenario_|TestWorkflowLifecycle_|TestPlanningRun_'`
  against `s24001_*`) rc=0.
- **Scenario suite.** `t001/scen.sh` with `go test -tags scenario -p 1 -count=1 -v ./service/scenariotest/...`
  against `s24001_scen_*` plus `s24001_reg_*` (registry-migrated).
  - Branch: 31 top-level failures, 1788 passes.
  - Base archive with the new tests: 34 failures, which are the same 31 plus the three new
    scenarios (red).
  - Subtest failure sets: none only on the branch.
  - The 31 are the INIT-023 §7f pre-existing set (`t001/scen-brfull.topfails`).
- **scenario-git-http job** (`t001/ci.sh`, every `go test` of the ci.yml job, `SPINE_TEST_DATABASE_URL`
  only): rc=0 and 0 skips.

  | Step | Passes |
  |---|---|
  | `TestGitHTTP_` | 87 |
  | `TestBootstrapInternalAdmin_` | 26 |
  | `TestWorkflowLifecycle_` | 154 |
  | workflow-policy integration | 1 |
  | step CAS | 12 |
  | step evidence | 24 |
  | `StatusLifecycle` | 37 |
  | status-lifecycle integration | 1 |
  | `TestGovernancePathGuard_` | 19 |
  | `TestGovernanceStore_Integration` | 1 |

- **git-floor job.** `docker run --rm --pull=never golang:1.26-bookworm` (git 2.39.5; the module
  cache was mounted read-only with `GOPROXY=off`) ran both ci.yml steps, rc=0. A verbose re-run of
  the two real-git TASK-001 tests confirmed they pass on 2.39.5.
- **golangci-lint v2.11.4.** `--new-from-rev 12376b55` on `./core/engine/ ./service/scenariotest/engine/`
  (plain and `integration`, plus `./service/scenariotest/scenarios/` under `scenario`): 0 issues.
  It first found an unused test-store field, fixed in `42b7e4c4`.
  `--enable-only=gosec --build-tags scenario` on the same packages: branch 0, base 0.
- `gofmt -l` on every changed `.go` file: clean.
- **Databases.** Every `s24001_*` database was dropped. No `ws_*` database or role was created.

### SMP compatibility gate (AC 12): PASS, no SMP change

SMP at `3addd44b`, `../spine` → this worktree at `42b7e4c4` (clean). Script `t001/smp.sh`.

- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`, `go vet -tags scenario ./scenariotest/...`
  and `go vet -tags faultinject ./...` all rc=0.
- **`go test -race -p 4 -count=1`** on the initiative §5 packages: `./cmd/customer-app/`,
  `./internal/customer/{githttp,gitread,projectionwriter,spinecore/...,spineembedded/...,stepevents,workspace/...,run/...}`
  and `./internal/engine/...`. The only direct `core/engine` importers are `spinecore` and
  `spineembedded`.
  - All pass except `internal/customer/gitread` `TestClient_ErrorContractEqualsCLIClient`
    (`dangling_gitlink`) and `TestClient_SpineReadFileRendersTheGitlinksPatch`.
  - Those are the two by-design gitread contract tests that already fail against `a560c210`
    (RULES-spine.md layout note; the INIT-023 wave 4 `ReadFile` change).
  - Proof that they are not this task: `go list -test -deps ./internal/customer/gitread/` reaches
    no package this diff changes. It reaches only `adapters/git`, `core/{artifact,branchprotect,domain,event,observe,queue,worktreefs,yamlsafe}`
    and `testutil`. The scaffold has no Go diff from `a560c210`, so the Spine code under test is
    byte-identical to `a560c210`.
- **Migrations.** `go run ./dev/apply-migrations -set platform -dir migrations` on
  `s24001_smp_it`: 116 versions. `-set spine-runtime` on `s24001_spine_runtime_it`: 26 versions.
- **ci.yml Spine-runtime step.** `go run ./dev/testgate -allow-skip 'ru_maxrss in KiB is the Linux contract' -- -race -count=1 -p 4 -timeout 10m ./internal/customer/{spinecore,spineembedded,runner,executionreadiness}/`
  (`TEST_SPINE_DB`, `TEST_PLATFORM_DB`, `TEST_PROVISIONING_DSN`=smp_prov): PASS, 2143 passed,
  0 failed. The 3 allowed skips are Linux `ru_maxrss` RSS tests.
- **githttp suite.** `go run ./dev/testgate -allow-skip 'receive-time RSS is measured with Linux ru_maxrss' -- -race -count=1 -tags integration ./internal/customer/githttp/`:
  PASS, 588 passed, 0 failed, 1 allowed skip.
- **Inert by construction.** SMP calls neither `WithStepEntryPolicy` nor `WithGovernanceGuard`
  (grep of `cmd/` and `internal/`, non-test). The zero value writes nothing and refuses nothing,
  and AC 4 pins that. `StartPlanningRun`'s signature is unchanged.

### Codex

`codex review --base init/INIT-024-scaffold` was run plain from the worktree, with no model
override.

- **Round 1** (at `79ed5e48`): review sub-session `01a16724-a1c4-73f2-9cac-2eee175c6eca`
  (`source {"subagent":"review"}`, cwd = this worktree), model **gpt-6.1-sol**.
  - Result: `findings: []`, "patch is correct", confidence 0.87.
  - Dispositions: nothing to fix.
- **Round 2** (at `42b7e4c4`, the final code): review sub-session
  `01a16740-a536-7ef0-be45-a820e37dbb48`, model **gpt-6.1-sol**.
  - Result: `findings: []`, "patch is correct", confidence 0.90.
  - Dispositions: nothing to fix.
- No finding was rejected, because none was raised. The adversarial pass above added
  `…RefusedOnEveryBindPath`, `…EveryPlanningStarterIsRefusedItsReview` and the binding-off
  carry-over (Deviation 3) before round 1. The commit that follows round 2 changes only this
  artifact.

### Review round 1 (independent reviewer at `b1a1548c`; fixes in `8fe04d62`)

Report: `tmp/init027-orchestration/i24/t001-rev/REVIEW.md` (SMP working tree, git-ignored). It found
no P1 or P2.

- **P3, fixed: the recorded starter was never checked against the step-entry resolver.**
  - The flaw: an embedder that recorded the wrong identifier (its platform user id instead of
    the workspace handle) silently reopened N-3. The reviewer's
    `TestRevPoC_WrongIdentifierSilentlyReopensN3` showed it: the record is `11111111-plat-uuid`, and
    the same person's handle `starter` claims and accepts, so the run completes and the artifact
    reaches `main`.
  - The fix: with `StepEntryPolicy.Actors` installed, `requirePlanningStarter` resolves the
    context's actor through `entryActor`, which is the bind lookup including its echo check.
    - An id the resolver does not resolve, or a resolver that answers for another id, is
      `ErrUnauthorized`.
    - Any other resolver error keeps its code (`ErrUnavailable` stays 503; untyped errors become
      `ErrInternal`).
    - All of this happens before any row, branch, git read or content write.
    - Without a resolver nothing changes: standalone ids are `auth.actors` rows, the same ones
      the binds look up.
    - Without separation of duties the resolver is not consulted.
  - Regression `TestSoD_PlanningStarterMustResolveThroughEntryResolver` (real git, strict policy;
    ids `11111111-plat-uuid`, `Starter` and `starter␠`):
    - Red at the scaffold base and at `b1a1548c`: "N-3 reopened: started as
      "11111111-plat-uuid" (recorded "11111111-plat-uuid"), the same person's handle "starter"
      claimed and accepted its own content (accept err=<nil>, run completed, main has the
      artifact: true)". At the base the record was `""`.
    - Green after the fix: `ErrUnauthorized`, no run, no content write, no `spine/plan/*` branch.
    - Logs: `t001/rr1-red-*.log`.
  - Also `TestSoD_PlanningStarterResolverOutcomes`, run for all three starters: known id,
    unknown id, an echo mismatch, `ErrUnavailable`, an untyped error, and separation of duties
    off. Red at both revisions for the four refusal cases.
  - Docs updated: `StepEntryPolicy.Actors` and `SeparationOfDuties` field docs,
    `security-model.md`, `KNOWN-LIMITATIONS.md` and `CHANGELOG.md` (embedders).
- **P3 (SMP-facing): the engine's early 401 surfaces as `CreateEntryError{RunStarted:true}`.**
  This is SMP code (`artifacts_createentry.go:186-188`), so it is not fixed here. It is recorded
  as SMP obligation 1: resolve the handle before the call.
- **P4, fixed: warning text.** The guard-only record-failure WARN now reads "governance guard:
  the planning run's starter was not recorded; if it is a workflow-lifecycle run, its workflow
  edit will need an operator approval" (CHANGELOG).
- **P4, fixed: AC 4 sub-cases.** `TestSoD_ZeroPolicyWritesNoStarter` now covers three
  configurations, each with all three starters and with and without an actor, and asserts 0
  commit_meta writes and no record in every case. They pass at the base too, as pins:
  - the zero value;
  - the guard on with the lifecycle exemption off (TASK-010's configuration);
  - a policy with every switch but separation of duties, plus a resolver.
- **False claim, corrected.** The "SMP impact" section said the refusal was "visible in SMP's
  tests". It is not. That section now says so, and lists the SMP obligations 1-5 for EPIC-004
  TASK-012's closing bump.
- **Gates re-run at `8fe04d62`** (logs in `t001/rr1/`, `t001/rr1-gates.out`, `t001/smp-rr1.out`):
  - `go vet` plain, `integration` and `scenario` `./...`: rc=0.
  - `go test -p 4 ./core/... ./service/...`: rc=0.
  - `go test -race -p 4 ./core/engine/ ./service/scenariotest/engine/`: rc=0.
  - **Scenario suite** (`t001/scen.sh … rr1full`): 31 failures and 1788 passes, with no top-level
    or subtest failure outside the base set (`scen-basefull.*`). The `-race` scenario subset is
    rc=0.
  - golangci-lint `--new-from-rev 12376b55`, plain, `integration` and `scenario`: 0 issues.
    gosec on the touched packages: 0. `gofmt -l` on the changed files: clean.
  - **SMP compatibility** (`../spine` = `8fe04d62`, clean):
    - `go build ./...` and `go vet` in all four variants: rc=0.
    - Both migrations applied.
    - `dev/testgate -race` on `spinecore`, `spineembedded`, `runner` and `executionreadiness`
      with `TEST_SPINE_DB`, `TEST_PLATFORM_DB` and `TEST_PROVISIONING_DSN`: PASS, 2143 passed and
      0 failed, with 3 allowed Linux-only `ru_maxrss` skips.
    - No `ws_*` database was left behind.
    - SMP calls neither `WithStepEntryPolicy` nor `WithGovernanceGuard`, so the resolver check is
      unreachable there.
- **Codex round 3** (at `8fe04d62`): `codex review --base init/INIT-024-scaffold`, run plain. Review
  sub-session `01a16768-72ba-76b3-a9af-17541e51ef56`, model **gpt-6.1-sol**. Result:
  `findings: []`, "patch is correct", confidence 0.88. Nothing to fix.

### Not run

Nothing in the gate list was skipped.
