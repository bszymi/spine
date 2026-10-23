---
id: TASK-001
type: Task
title: "Separation of duties counts a planning run's starter as an author"
status: Pending
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
  `domain.WithActor(ctx, &domain.Actor{ActorID: <member handle>})` before `StartPlanningRun`. The
  handle must be the exact string SMP's step-entry resolver later binds (`human-<uuid>`), because
  the comparison is exact. Without it every planning start is refused (fail closed, visible in
  SMP's tests).
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
