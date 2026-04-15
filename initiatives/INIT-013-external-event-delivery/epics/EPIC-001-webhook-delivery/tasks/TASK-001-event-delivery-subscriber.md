---
id: TASK-001
type: Task
title: "Implement event delivery subscriber"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
---

# TASK-001 — Implement Event Delivery Subscriber

## Purpose

Create the bridge between Spine's internal EventRouter and external delivery. A new `DeliverySubscriber` subscribes to all event types on the EventRouter and persists them to a delivery queue for async processing.

## Deliverable

- `internal/delivery/subscriber.go` — subscribes to EventRouter, writes to delivery queue
- Subscribe to all event types (domain + operational)
- Each event becomes a delivery entry with: event_id, event_type, payload, delivery_status, created_at
- Delivery entries are written to a persistent queue table (not the in-memory queue)
- The subscriber is fire-and-forget from the EventRouter's perspective — never blocks event emission

## Acceptance Criteria

- All emitted events appear in the delivery queue within 100ms
- EventRouter performance is not affected (async write)
- Delivery entries are idempotent by event_id
