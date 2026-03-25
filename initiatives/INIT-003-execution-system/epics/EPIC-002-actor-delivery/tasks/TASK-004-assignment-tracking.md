---
id: TASK-004
type: Task
title: Assignment Tracking and Polling
status: Completed
epic: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
initiative: /initiatives/INIT-003-execution-system/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-003-execution-system/epics/EPIC-002-actor-delivery/epic.md
---

# TASK-004 — Assignment Tracking and Polling

## Purpose

Track assignment delivery status and provide a polling endpoint so actors can query their pending work without requiring push-based notification.

## Deliverable

- Assignment status tracking in store (pending, delivered, acknowledged, completed, failed)
- `GET /assignments?actor_id=X` — polling endpoint for pending assignments
- Delivery status updates on assignment lifecycle events
- Assignment expiry for unacknowledged assignments

## Acceptance Criteria

- Assignment status is tracked from creation through completion
- Actors can poll for their pending assignments via API
- Assignments that are not acknowledged within a timeout are re-assignable
- Assignment history is queryable for debugging
