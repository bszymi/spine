---
id: EPIC-001
type: Epic
title: "Webhook Delivery"
status: Completed
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/initiative.md
---

# EPIC-001 — Webhook Delivery

---

## Purpose

Deliver Spine events to external HTTP endpoints (webhooks). When an event occurs (step assigned, run completed, artifact created, etc.), Spine POSTs the event payload to configured webhook URLs. This is the first and most critical delivery mechanism — it enables the SMP runner dispatch and customer automation integrations.

## Key Work Areas

- Subscribe to internal EventRouter for configured event types
- Format webhook payload (event envelope + HMAC signature)
- POST to configured URLs with timeout and error handling
- HMAC-SHA256 signing with per-subscription secret
- Delivery status tracking (success, failed, pending retry)
- Circuit breaker for consistently failing endpoints

## Acceptance Criteria

- Events delivered to webhook URLs within 1 second of emission
- Payload includes `X-Spine-Signature` header with HMAC-SHA256
- Webhook delivery does not block the internal event pipeline
- Delivery is async — events are queued, not sent inline
