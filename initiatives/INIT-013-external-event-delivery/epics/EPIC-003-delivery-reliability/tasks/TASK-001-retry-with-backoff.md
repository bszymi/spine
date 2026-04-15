---
id: TASK-001
type: Task
title: "Implement retry with exponential backoff"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-003-delivery-reliability/epic.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-003-delivery-reliability/epic.md
---

# TASK-001 — Implement Retry with Exponential Backoff

## Purpose

When a webhook delivery fails (timeout, 5xx, network error), retry with increasing delays. Domain events retry up to 5 times; operational events retry up to 2 times.

## Deliverable

- On delivery failure: increment attempt_count, calculate next_retry_at using exponential backoff (1s, 2s, 4s, 8s, 16s)
- Dispatcher skips entries where next_retry_at is in the future
- Distinguish retryable errors (5xx, timeout, network) from permanent errors (4xx except 429)
- 429 Too Many Requests: use Retry-After header if present, otherwise backoff
- After max retries: set status to "dead" (dead letter)

## Acceptance Criteria

- Transient failures are retried with increasing delays
- 4xx errors (except 429) are not retried
- Dead letter entries are preserved and queryable
- Backoff delays are correct (1s, 2s, 4s, 8s, 16s)
