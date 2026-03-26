---
id: TASK-002
type: Task
title: Operational Event Emission
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-009-event-observability/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-009-event-observability/epic.md
---

# TASK-002 — Operational Event Emission

## Purpose

Wire operational event emission for system visibility events that are not reconstructible from Git.

## Deliverable

Emit the following operational events:

- `divergence_started` — when divergence context is created
- `convergence_completed` — when convergence evaluation finishes
- `engine_recovered` — when a run is recovered from crash state
- `projection_synced` — when projection service completes a sync cycle
- `validation_passed` / `validation_failed` — when validation engine runs
- `step_assignment_failed` — when actor assignment delivery fails

## Acceptance Criteria

- All listed operational events are emitted at correct points
- Events include relevant payload for debugging and monitoring
- Operational events are distinguishable from domain events
- Events are routable to consumers via the event router
