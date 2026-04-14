---
id: TASK-005
type: Task
title: "Expose step execution acknowledge endpoint"
status: Completed
work_type: implementation
created: 2026-04-14
epic: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
initiative: /initiatives/INIT-012-unified-actor-api/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/epic.md
  - type: depends_on
    target: /initiatives/INIT-012-unified-actor-api/epics/EPIC-001-actor-api-endpoints/tasks/TASK-004-filter-steps-by-actor-type.md
---

# TASK-005 — Expose step execution acknowledge endpoint

---

## Context

The step execution state machine already supports the `actor.acknowledged` trigger which transitions a step from `assigned` → `in_progress` and sets `started_at`. Currently this transition only happens implicitly when an actor submits a result from `assigned` state (auto-acknowledge in `SubmitStepResult`).

All non-human actors (runners and AI agents) need an explicit acknowledge endpoint so they can signal "I'm starting this step" before execution begins. This prevents a second process sharing the same actor identity from also attempting the same step — the acknowledge call is atomic, only one wins. The same concurrency risk applies to AI agents as to runners: multiple agent instances could be spawned with the same credentials.

## Deliverable

### New endpoint

`POST /api/v1/steps/{execution_id}/acknowledge`

Request body:
```json
{
  "actor_id": "actor-59ce0823"
}
```

### Behavior

- Validates the actor is the assigned actor for this step execution
- Fires the `actor.acknowledged` trigger on the step state machine
- Transitions step from `assigned` → `in_progress`
- Sets `started_at` timestamp
- Emits `EventStepStarted` event
- Returns 200 with updated step execution status
- Returns 409 if step is already `in_progress` or terminal (second runner gets conflict)
- Returns 403 if actor_id doesn't match the assigned actor

### Gateway handler update

Also update the `handleListStepExecutions` handler to allow querying `status=in_progress` in addition to `waiting` and `assigned`. Runners need to resume in-progress steps after restart.

## Acceptance Criteria

- POST /api/v1/steps/{id}/acknowledge transitions assigned → in_progress
- Returns 409 if step is not in assigned state
- Returns 403 if actor doesn't match assignment
- Step execution query accepts status=in_progress
- Existing auto-acknowledge on submit continues to work unchanged
- Works for all non-human actor types: automated_system and ai_agent
