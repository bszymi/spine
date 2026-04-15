---
id: TASK-001
type: Task
title: "Implement SSE event stream endpoint"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-004-streaming-and-pull/epic.md
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-004-streaming-and-pull/epic.md
---

# TASK-001 — Implement SSE Event Stream Endpoint

## Purpose

Allow consumers to receive events in real-time without exposing a public endpoint. The consumer connects to Spine and events are pushed over the HTTP connection using Server-Sent Events.

## Deliverable

- `GET /api/v1/events/stream` — SSE endpoint
- Query params: `types` (comma-separated event types), `workspace_id`
- Auth: Bearer token (workspace-scoped)
- Each SSE message: `id: {event_id}\nevent: {event_type}\ndata: {json_payload}\n\n`
- Reconnection: consumer sends `Last-Event-ID` header, Spine replays missed events from delivery queue
- Heartbeat: send comment (`: keepalive`) every 30 seconds to detect dead connections

## Acceptance Criteria

- Events delivered in real-time over SSE connection
- Reconnection replays missed events (no data loss)
- Heartbeat keeps connection alive through proxies
- Workspace-scoped (consumer only sees their events)
