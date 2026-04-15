---
id: EPIC-004
type: Epic
title: "Streaming and Pull Delivery"
status: Pending
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/initiative.md
  - type: depends_on
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-002-event-subscription-config/epic.md
---

# EPIC-004 — Streaming and Pull Delivery

---

## Purpose

Webhooks require consumers to expose a public endpoint. Not all consumers can do this (local development, serverless functions, firewalled environments). This epic adds two additional delivery mechanisms:

1. **SSE (Server-Sent Events)** — consumer connects to Spine, events are pushed over the HTTP connection
2. **Pull API** — consumer queries an event log endpoint for events since a cursor

A future extension can add Kafka/NATS connectors for high-throughput streaming.

## Key Work Areas

### SSE Stream Endpoint
- `GET /api/v1/events/stream?types=step_assigned,run_completed` — SSE endpoint
- Consumer connects with auth token, receives events as they happen
- Reconnection support via `Last-Event-ID` header
- Per-workspace scoping

### Pull Event Log
- `GET /api/v1/events?after=<cursor>&types=<types>&limit=100` — paginated event log
- Cursor-based pagination (event_id or timestamp)
- Consumer polls at their own pace
- Events retained for configurable period (default 7 days)

### Future: Kafka/NATS Connector
- Subscription target_type "kafka" with broker config in metadata
- Delivery system publishes to topic instead of HTTP POST
- Same subscription model, different transport

## Acceptance Criteria

- SSE endpoint delivers events in real-time over HTTP
- Pull API returns events since a cursor with consistent ordering
- Both mechanisms use the same subscription model as webhooks
- Consumer can switch between webhook/SSE/pull without reconfiguring event filters
