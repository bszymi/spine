---
id: TASK-003
type: Task
title: "Implement delivery history API"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-003-delivery-reliability/epic.md
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-003-delivery-reliability/epic.md
---

# TASK-003 — Implement Delivery History API

## Purpose

Queryable delivery history for debugging. Workspace admins can check if their webhooks received events, see failure details, and replay dead-lettered events.

## Deliverable

Endpoints under `/api/v1/subscriptions/{id}/deliveries`:

- `GET /` — list delivery attempts (paginated, filterable by status, event_type, date range)
- `GET /{delivery_id}` — get delivery details (request/response, timing, error)
- `POST /{delivery_id}/replay` — re-enqueue a failed/dead delivery for retry

Also: `GET /api/v1/subscriptions/{id}/stats` — delivery success rate, avg latency, queue depth.

## Acceptance Criteria

- Delivery history is queryable per subscription
- Failed deliveries show error details (status code, response body snippet)
- Dead letter events can be replayed
- Stats endpoint shows health of each subscription
