---
id: TASK-002
type: Task
title: "Implement pull-based event log API"
status: Pending
created: 2026-04-15
epic: /initiatives/INIT-013-external-event-delivery/epics/EPIC-004-streaming-and-pull/epic.md
initiative: /initiatives/INIT-013-external-event-delivery/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-013-external-event-delivery/epics/EPIC-004-streaming-and-pull/epic.md
---

# TASK-002 — Implement Pull-Based Event Log API

## Purpose

Allow consumers to fetch events at their own pace. Useful for batch processing, backfill, and environments where persistent connections aren't practical.

## Deliverable

- `GET /api/v1/events` — paginated event log
- Query params: `after` (cursor — event_id or timestamp), `types` (filter), `limit` (default 50, max 1000), `workspace_id`
- Response: `{events: [...], next_cursor: "...", has_more: bool}`
- Events ordered by timestamp (stable sort by event_id for ties)
- Auth: Bearer token (workspace-scoped)
- Events retained for configurable period (default 7 days, env `SPINE_EVENT_RETENTION`)
- Background cleanup of expired events

## Acceptance Criteria

- Cursor-based pagination works correctly (no skipped/duplicated events)
- Events filtered by type and workspace
- Retention cleanup runs automatically
- Consumer can poll at any interval without missing events (as long as within retention window)
