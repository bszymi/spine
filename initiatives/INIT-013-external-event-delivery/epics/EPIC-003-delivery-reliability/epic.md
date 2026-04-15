---
id: EPIC-003
type: Epic
title: "Delivery Reliability"
status: Pending
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/initiative.md
  - type: blocked_by
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-001-webhook-delivery/epic.md
---

# EPIC-003 — Delivery Reliability

---

## Purpose

Make event delivery robust against endpoint failures, network issues, and Spine restarts. Domain events must be delivered at-least-once. Operational events are best-effort but should still retry on transient failures.

## Key Work Areas

- Retry with exponential backoff (1s → 2s → 4s → 8s → 16s, max 5 retries)
- Dead letter handling for permanently failed deliveries
- Circuit breaker for endpoints that fail consistently
- Delivery ordering guarantees (per-subscription, best-effort)
- Metrics and observability (delivery latency, failure rate, queue depth)
- Queue drain on Spine shutdown (finish in-flight, persist remaining)
- Delivery history API for debugging

## Acceptance Criteria

- Domain events retry up to 5 times with exponential backoff
- After max retries, events move to dead letter (queryable, replayable)
- Circuit breaker trips after 10 consecutive failures, half-opens after 60 seconds
- Delivery latency and failure rate are logged
- Queue depth is observable (log or metric)
