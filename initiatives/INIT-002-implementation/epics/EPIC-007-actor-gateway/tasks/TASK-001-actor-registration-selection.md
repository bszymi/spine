---
id: TASK-001
type: Task
title: Actor Registration and Selection
status: Pending
epic: /initiatives/INIT-002-implementation/epics/EPIC-007-actor-gateway/epic.md
initiative: /initiatives/INIT-002-implementation/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-002-implementation/epics/EPIC-007-actor-gateway/epic.md
---

# TASK-001 — Actor Registration and Selection

## Purpose

Implement actor registration (runtime config) and the selection algorithm for step assignment.

## Deliverable

- Actor record management (CRUD in runtime config or store)
- Actor status lifecycle (active, suspended, deactivated per Actor Model §7)
- Selection algorithm: filter by type → capability → role → availability → strategy (per Actor Model §4.2)
- Selection strategies: explicit, any_eligible, round_robin
- Assignment failure handling (no eligible actor → event + retry)

## Acceptance Criteria

- Actors can be registered, suspended, deactivated
- Selection correctly filters by all criteria
- Round-robin distributes evenly across eligible actors
- Assignment fails gracefully when no actor is eligible
- Deactivated actors are never assigned
- Unit tests cover all selection scenarios and edge cases
