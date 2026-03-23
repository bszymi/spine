---
id: TASK-001
type: Task
title: Domain Event Emission
status: Pending
epic: /initiatives/INIT-003-execution-system/epics/EPIC-009-event-observability/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-009-event-observability/epic.md
---

# TASK-001 — Domain Event Emission

## Purpose

Wire domain event emission into the engine orchestrator and artifact service so all defined domain events are emitted at the appropriate points.

## Deliverable

Emit the following events at the correct points:

- `run_started` — when run transitions to active
- `run_completed` — when run transitions to completed
- `run_failed` — when run transitions to failed
- `run_cancelled` — when run is cancelled
- `run_paused` / `run_resumed` — on pause/resume transitions
- `step_assigned` — when step is assigned to actor
- `step_started` — when step transitions to in_progress
- `step_completed` — when step completes successfully
- `step_failed` — when step fails permanently
- `retry_attempted` — when a step retry is initiated

## Acceptance Criteria

- All listed events are emitted at the correct lifecycle points
- Events include required payload (IDs, timestamps, actor, trace context)
- Events are routable via the existing event router
- No events are emitted for invalid state transitions
