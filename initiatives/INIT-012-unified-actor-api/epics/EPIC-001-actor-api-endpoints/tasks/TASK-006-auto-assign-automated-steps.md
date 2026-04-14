---
id: TASK-006
type: Task
title: "Auto-assign automated steps to the correct actor"
status: Completed
work_type: implementation
created: 2026-04-14
epic: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
  - type: depends_on
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/tasks/TASK-003-eligible-actor-ids.md
  - type: depends_on
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/tasks/TASK-005-expose-step-acknowledge-endpoint.md
---

# TASK-006 — Auto-assign automated steps to the correct actor

---

## Context

When a workflow step has `mode: automated_only`, Spine transitions it to `assigned` status automatically. However, it currently sets `actor_id = null` — the step is assigned to no one. Runners query for steps assigned to their actor ID and never see these steps.

A workspace can have multiple automated system actors (different sizes/capabilities). The workflow or step execution may specify which actor should handle the step, or leave it to the default.

## The full auto-assignment process

When a step transitions to `assigned` for an `automated_only` step, Spine must resolve the target actor:

### Case 1: Step specifies an actor via `eligible_actor_ids`

The step execution has `eligible_actor_ids` set (from TASK-003). This means the workflow definition or the run start explicitly named which actor(s) should handle this step.

**Action:** Assign to the first available actor from `eligible_actor_ids`. Set `actor_id` on the step execution.

If `eligible_actor_ids` contains multiple actors, pick one (e.g., first in list, or round-robin — simple strategy is fine for now).

### Case 2: Step does NOT specify an actor (default)

The step has `eligible_actor_types: [automated_system]` but no `eligible_actor_ids`. This is the common case — the workflow says "any automated system can do this."

**Action:** Assign to the workspace's default automated system actor. Resolution:

1. Look up the workspace from the run
2. Find actors of type `automated_system` registered in that workspace
3. If exactly one exists — assign to it (the default)
4. If multiple exist — assign to the one marked as default (or the first registered, if no default is configured)
5. If none exist — leave as `assigned` with `actor_id = null` and log a warning. The step will eventually timeout. The workspace admin needs to register a runner.

### Case 3: Step specifies an actor type other than automated_system

Same logic applies for `ai_agent` type steps. Look up available actors of that type in the workspace and assign accordingly.

## Where this runs

This logic should execute in the step activation path — when the engine creates a new step execution and determines it should be auto-assigned (based on `mode: automated_only`).

The assignment must:
- Set `actor_id` on the step execution record
- Create an `actor_assignment` record (status: active)
- The step remains in `assigned` status (the runner will acknowledge → in_progress)

## Runner flow after this fix

1. Runner polls: `GET /execution/steps?actor_id={my_id}&status=assigned`
2. Sees steps assigned to it
3. Acknowledges: `POST /steps/{id}/acknowledge` → `in_progress`
4. Executes the step
5. Submits: `POST /steps/{id}/submit` → `completed`/`failed`

## Acceptance Criteria

- Automated steps with `eligible_actor_ids` are assigned to a matching actor
- Automated steps without `eligible_actor_ids` are assigned to the workspace's default actor of the required type
- `actor_id` is set on the step execution record
- An `actor_assignment` record is created
- Runners can discover their assigned steps via the existing query endpoint
- Works for both `automated_system` and `ai_agent` actor types
- If no actor of the required type exists in the workspace, step stays assigned with null actor_id (graceful degradation)
