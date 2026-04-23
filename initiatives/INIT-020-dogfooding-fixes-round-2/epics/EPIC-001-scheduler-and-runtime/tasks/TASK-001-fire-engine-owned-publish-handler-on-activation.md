---
id: TASK-001
type: Task
title: "Fire engine-owned publish handler on step activation"
status: Completed
epic: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
work_type: bugfix
created: 2026-04-23
last_updated: 2026-04-23
links:
  - type: parent
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-015-engine-driven-commit-outcome.md
  - type: related_to
    target: /initiatives/INIT-008-dogfooding-fixes/epics/EPIC-001-scheduler-and-runtime/tasks/TASK-016-workflow-publish-step-audit.md
  - type: related_to
    target: smp:architecture/adrs/ADR-010-single-merge-path-spine-driven-commit.md
---

# TASK-001 — Fire engine-owned publish handler on step activation

---

## Purpose

Spine `INIT-008/EPIC-001/TASK-015` ("Engine-owned publish step: internal execution mode + rename from commit", Completed 2026-04-22) introduced `type: internal` + `execution.mode: spine_only` + `execution.handler: merge` in `workflows/task-default.yaml`, and says it registered the `merge` handler against `engine.MergeRunBranch`. Its acceptance criteria include:

- "A run on `task-default.yaml` completes end-to-end with zero runner dispatch events for the `publish` step."
- "The dispatcher MUST NOT emit a runner dispatch event for `spine_only` steps. The scheduler's eligibility path skips them entirely."
- "Step execution audit shows the engine actor (e.g., `actor-engine-merge`) as producer of the `published` outcome."

These are not met in practice. Observed against the SMP workspace on 2026-04-23:

```
09:02:10.324  step progressed  completed_step=review  next_step=publish
09:02:10.357  assignment delivered  assignment_id=run-333c1123-publish-1  actor_id=""  step_id=publish
09:02:10.359  step activated    step_id=publish
09:02:10.594  runner POST /api/v1/steps/run-333c1123-publish-1/acknowledge  → 403
09:02:10.594+ (nothing further — publish stays at status=assigned indefinitely)
```

Two distinct regressions vs TASK-015's contract:

1. **Assignments are being delivered for `spine_only` steps.** The `assignment delivered` line fired with `actor_id=""` — Spine is still creating assignments for internal steps and emitting `step_assigned` events on them. Runners pick these up from downstream event streams (SMP's `event_receiver.go` enqueued this one) and try to acknowledge, at which point Spine correctly returns 403. TASK-015 said this path "MUST NOT" happen.
2. **The merge handler is never invoked.** After `step activated`, nothing in Spine calls `engine.MergeRunBranch` (or the advance-workflow path TASK-015 described). The step stays at `assigned` forever. No `merge_failed` outcome, no `published` outcome — dead on arrival.

The combined effect: every SMP task run on `task-default` wedges at `publish` and has to be hand-merged on the SMP side. The three most recent SMP tasks (`TASK-010`, `TASK-014`, `TASK-001`) were all landed via a manual `git merge` + `Mark … Completed (merged manually)` commit. That is the SMP operator pattern right now, but it defeats the entire purpose of ADR-010.

---

## Deliverable

### 1. Stop emitting assignments / `step_assigned` for `spine_only` steps

Wherever the scheduler creates `step_executions` rows with `status=assigned` on activation, branch on `execution.mode`:

- For `type: internal` + `mode: spine_only`: do **not** create a regular assignment; do **not** emit a `step_assigned` event; instead, create the execution record in whatever status the engine will drive directly (e.g. `in_progress` keyed to a stable engine actor such as `actor-engine-merge`, or a dedicated `engine_owned` status — pick whichever fits the existing state machine with least churn).
- For everything else: unchanged.

Confirm with a grep: `assignment delivered` and `step_assigned` events on a publish step should both stop appearing in the Spine log after this change.

### 2. Invoke the registered handler synchronously on activation

The engine's step-activation path should, for `mode: spine_only`, look up `execution.handler` in the handler registry and invoke it immediately. Concretely:

- On successful handler return: advance the step to its success outcome (`published` for `merge`), run `applyCommitStatus`, and transition the run through its next step (typically `end`).
- On permanent handler failure (non-retryable): advance the step to its failure outcome (`merge_failed` for `merge`), sending the run back to whatever `next_step` the workflow declares (for `task-default`, `execute`). Distinguish from "never fired" so the failure surface is real.
- On transient / retryable failure (`git.GitError.IsRetryable` per TASK-015's description): keep the existing scheduler-retry semantics. The step stays assigned-to-engine and the scheduler re-invokes the handler, same behaviour as today when merge transiently fails.

Synchronous invocation is the simplest correct shape, but an immediately-scheduled internal dispatch (same event loop tick) is acceptable if the engine's concurrency model prefers it — the observable contract is that `published` / `merge_failed` appears on the step within seconds, not never.

### 3. Diagnosable log line on invocation

TASK-015's acceptance included "step execution audit shows the engine actor as producer of the `published` outcome." Add an explicit `msg: "internal handler invoked"` (or similar) log line at handler entry, keyed by `workflow_id`, `step_id`, `handler`, `run_id`, `execution_id`. Without it, the next time this wedges it will again look like "nothing happened" instead of "handler crashed mid-flight."

### 4. Scenario coverage (new)

`scenariotest/scenarios/standard_run_test.go` (or add a new scenario test if that one has been split): exercise a full `task-default` run end-to-end and assert:

- `publish` step has exactly one execution record.
- The execution's actor is the engine actor (`actor-engine-merge` or whatever TASK-015 settled on).
- No `step_assigned` events were emitted for the publish step.
- The workflow reached `end` via the `published` outcome.
- The task artifact on `main` has `status: Completed` as a result of the merge handler's `applyCommitStatus` pass.

This scenario is the regression guard that TASK-015 should have had.

---

## Acceptance Criteria

- A fresh Spine run of `task-default` on any SMP task completes end-to-end — `execute → validate → verify → review → publish → end` — with `status: Completed` on the task artifact on `main`, no manual `git merge`, no manual status-flip commit.
- For that run, a grep of Spine logs shows:
  - `step activated` for `publish`.
  - An explicit `internal handler invoked` (or equivalent) log line for the `merge` handler.
  - Either `handler success` + `step progressed completed_step=publish` or `handler failure` + `next_step=execute` (depending on which path the run took) — **not silence**.
  - Zero `assignment delivered` / `step_assigned` emissions for the publish step.
- The new scenario test passes locally and in CI, and fails deterministically if the handler invocation path is broken.
- SMP's downstream runner dispatch queue receives no publish-step events after this lands. SMP already filters `spine_only` steps at the enqueue point defensively, but that SMP-side filter must be unnecessary once this is fixed.

## Dependencies

- None blocking. The handler registration, schema validation, and workflow YAML changes from TASK-015 are already on `main`. This task is the runtime-invocation follow-up.

## Out of scope

- Generalising `spine_only` to a new handler beyond `merge`. The registry shape can admit more, but this task does not introduce any.
- Changes to how Spine pushes to git remotes (push path and credentials remain per ADR-009).
- Auditing the other workflows for missed `publish` migration — TASK-016 already concluded that audit (Cancelled) and its finding still holds: the other seven workflows use the implicit `commit:`-on-terminal-outcome pattern and do not need a `publish` step.
- Reopening or amending TASK-015. It landed schema + rename work that was needed; the runtime wiring just wasn't exercised end-to-end before closure. That's a process lesson captured in INIT-020's success criterion #3, not a reason to rewrite history.

## Operational notes

Until this lands, SMP operators should continue using the manual-merge pattern that TASK-010, TASK-014, and SMP TASK-001 established: after review acceptance wedges at `publish`, merge the Spine branch into main locally, flip the task artifact's status, commit with "`Mark … Completed (merged manually)`", and push. The stuck Spine run is harmless — leave it in place.

SMP has already added a defensive filter on its side (`internal/customer/runner/step_filter.go`) that excludes `spine_only` steps from the runner dispatch queue so the runner stops generating 403 noise on acknowledge. That filter remains useful as a belt-and-braces check even after this task lands; it should not be removed as part of this work.
