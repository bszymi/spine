---
id: TASK-014
type: Task
title: "[Low priority] Fix system.rebuild to async 202 with rebuild_id"
status: Completed
epic: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
initiative: /initiatives/INIT-005-api-spec-conformance/initiative.md
links:
  - type: parent
    target: /initiatives/INIT-005-api-spec-conformance/epics/EPIC-001-spec-conformance/epic.md
---

# TASK-014 — [Low priority] Fix system.rebuild to async 202 with rebuild_id

## Purpose

The spec says `POST /system/rebuild` returns 202 with `{status: "started", rebuild_id}` indicating an async operation. The current implementation runs synchronously and returns 200 `{status: "completed"}`. For large repos, sync rebuild will time out.

## Deliverable

- Generate a `rebuild_id` and start the rebuild in a background goroutine
- Return 202 immediately with `{status: "started", rebuild_id}`
- Update `GET /system/rebuild/{rebuild_id}` to return proper `RebuildStatusResponse` with `rebuild_id`, `status`, `started_at`, `completed_at`, `artifacts_processed`, `error_detail`
- Store rebuild state for status polling

## Acceptance Criteria

- `POST /system/rebuild` returns 202 with `rebuild_id`
- `GET /system/rebuild/{rebuild_id}` returns current rebuild status
- Rebuild runs asynchronously
- Status transitions: in_progress -> completed (or failed)
