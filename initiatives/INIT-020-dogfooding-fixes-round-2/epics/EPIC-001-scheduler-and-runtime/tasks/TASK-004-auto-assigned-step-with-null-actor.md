---
id: TASK-004
type: Task
title: "Auto-assigned execute step with null actor_id is unrecoverable"
status: Pending
epic: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
initiative: /initiatives/INIT-020-dogfooding-fixes-round-2/initiative.md
work_type: bugfix
created: 2026-04-30
last_updated: 2026-04-30
links:
  - type: parent
    target: /initiatives/INIT-020-dogfooding-fixes-round-2/epics/EPIC-001-scheduler-and-runtime/epic.md
---

# TASK-004 — Auto-assigned execute step with null actor_id is unrecoverable

---

## Purpose

When a run is started against `task-default.yaml`, the entry `execute` step lands in `status=assigned` with `actor_id=NULL`:

```
GET /api/v1/runs/run-3bbd2d59 →
{
  "step_executions": [
    { "execution_id": "run-3bbd2d59-execute-1", "step_id": "execute",
      "status": "assigned", "actor_id": null }
  ]
}
```

This phantom assignment is unrecoverable through the API:

- `POST /runs/{id}/steps/execute/assign` with body `{"actor_id":"bszymi"}` →
  `{"error":"invalid trigger \"step.assign\" for assigned step"}`. The
  workflow state machine rejects the transition because the step is
  already `assigned`, even though no concrete actor is bound.
- `POST /steps/{exec_id}/submit` with `{"outcome_id":"completed", ...}` →
  the call returns 200, the step transitions to `failed` with
  `outcome_id=null`, and a fresh `execute-2` is spawned in `waiting`. The
  IngestResult path silently rejects the submission because the bound
  actor is empty and the gateway-level ownership check passes only
  because `exec.ActorID == ""` is treated as "skip".

Net effect: the only way to make progress is to cancel the run and start
a new one, then race to submit before the auto-assignment fires — or to
manually `UPDATE runtime.step_executions SET status = 'waiting' WHERE
execution_id = '…'` and call `/assign` from there.

The "auto-claim execute" behavior is presumably intentional for solo-actor
ergonomics (the run creator is implicitly the executor), but the binding
is incomplete: status flips to `assigned` without writing the actor, and
the workflow trigger map doesn't allow `step.assign` to override an
already-assigned step.

## Deliverable

Pick one and implement:

**Option A: Bind the actor at auto-assignment time.** When the engine
auto-claims `execute` on run start, write the run's creator
(`run.created_by` / authenticated actor) into `step_executions.actor_id`
so the gateway-level ownership check has something to validate against.

**Option B: Don't auto-assign on run start.** Leave the entry step in
`waiting` and require an explicit `POST /assign`. This is more verbose
but keeps the state machine consistent.

**Option C: Allow `step.assign` to re-bind an `assigned` step when
`actor_id IS NULL`.** Smallest blast radius — keeps the auto-assignment
behavior but allows recovery.

Author's recommendation: A. The current system clearly intends to
auto-claim; the bug is just that it forgets to write the actor_id.

## Acceptance Criteria

- After `POST /api/v1/runs` returns, the entry `execute` step's
  `actor_id` is non-null (whichever option is chosen, the resulting
  state must be self-consistent).
- `POST /steps/{exec_id}/submit` with the expected actor authenticated
  works on the first try without spawning a `failed` execute-1 + fresh
  execute-2.
- If Option B is chosen, `/assign` works as the first transition out of
  `waiting`, and submitting from `waiting` returns a typed error rather
  than 200 + silent failure.
- A scenariotest exercises the entry-step lifecycle end-to-end against
  `task-default.yaml` and asserts the run advances to `validate` with
  exactly one `execute` execution row.

## Out of Scope

- Changing the workflow state machine for non-entry steps.
- Multi-actor / claim-from-pool workflows (`candidates` / `/claim` API).
