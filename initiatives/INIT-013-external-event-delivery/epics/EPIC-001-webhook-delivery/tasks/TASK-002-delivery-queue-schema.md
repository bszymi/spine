---
id: TASK-002
type: Task
title: "Create delivery queue database schema"
status: Completed
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
---

# TASK-002 — Create Delivery Queue Database Schema

## Purpose

Persistent storage for event delivery entries. Events are written here by the subscriber and read by the webhook dispatcher for delivery. Separate from the in-memory queue used by the internal EventRouter.

## Deliverable

Migration with tables:

- `event_delivery_queue` — pending deliveries: event_id, subscription_id, event_type, payload (jsonb), status (pending/delivering/delivered/failed/dead), attempt_count, next_retry_at, last_error, created_at, delivered_at
- `event_delivery_log` — delivery history: delivery_id, subscription_id, event_id, status_code, duration_ms, error, created_at

Indexes on (status, next_retry_at) for dispatcher reads and (subscription_id, created_at) for history queries.

## Acceptance Criteria

- Migration applies cleanly
- Queue supports atomic claim (SELECT FOR UPDATE SKIP LOCKED)
- Dead letter entries are preserved for debugging
- Log table supports efficient per-subscription history queries
