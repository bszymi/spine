---
id: TASK-003
type: Task
title: "Implement webhook dispatcher"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
  - type: depends_on
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/tasks/TASK-002-delivery-queue-schema.md
---

# TASK-003 — Implement Webhook Dispatcher

## Purpose

Background worker that reads from the delivery queue and POSTs events to configured webhook URLs. Handles HMAC signing, timeouts, and status tracking.

## Deliverable

- `internal/delivery/webhook_dispatcher.go`
- Reads pending entries from `event_delivery_queue`
- For each entry: look up subscription → build webhook request → POST → record result
- HMAC-SHA256 signing: `X-Spine-Signature: sha256=<hex>` using subscription's signing secret
- Request headers: Content-Type application/json, X-Spine-Event (event type), X-Spine-Delivery (delivery ID)
- HTTP timeout: 10 seconds per request
- Configurable concurrency (default: 5 concurrent deliveries)
- Record delivery result in `event_delivery_log`
- On success: mark delivered. On failure: schedule retry (see EPIC-003)

## Acceptance Criteria

- Webhook delivered within 1 second of event entering queue (under normal load)
- HMAC signature is verifiable by consumer using shared secret
- Failed deliveries logged with status code and error
- Concurrent delivery doesn't exceed configured limit
- Dispatcher handles endpoint timeouts gracefully (doesn't block queue)
